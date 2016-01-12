package handler

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	skyAsset "github.com/oursky/skygear/asset"
	"github.com/oursky/skygear/router"
	"github.com/oursky/skygear/skydb"
	"github.com/oursky/skygear/skydb/skydbconv"
	"github.com/oursky/skygear/skyerr"
)

// used to clean file path
var sanitizedPathRe = regexp.MustCompile(`\A[/.]+`)

func clean(p string) string {
	sanitized := strings.Replace(sanitizedPathRe.ReplaceAllString(path.Clean(p), ""), "..", "", -1)
	// refs #426: S3 Asset Store is not able to put filename with `+` correctly
	sanitized = strings.Replace(sanitized, "+", "", -1)
	return sanitized
}

func validateAssetGetRequest(assetStore skyAsset.Store, fileName string, expiredAtUnix int64, signature string) skyerr.Error {
	// check whether the request is expired
	expiredAt := time.Unix(expiredAtUnix, 0)
	if timeNow().After(expiredAt) {
		return skyerr.NewError(skyerr.PermissionDenied, "Access denied")
	}

	// check the signature of the URL
	signatureParser := assetStore.(skyAsset.SignatureParser)
	valid, err := signatureParser.ParseSignature(signature, fileName, expiredAt)
	if err != nil {
		log.Errorf("Failed to parse signature: %v", err)

		return skyerr.NewError(skyerr.PermissionDenied, "Access denied")
	}

	if !valid {
		return skyerr.NewError(skyerr.InvalidSignature, "Invalid signature")
	}
	return nil
}

type AssetGetURLHandler struct {
	AssetStore skyAsset.Store `inject:"AssetStore"`
}

func (h *AssetGetURLHandler) Setup() {
	return
}

func (h *AssetGetURLHandler) GetPreprocessors() []router.Processor {
	return nil
}

func (h *AssetGetURLHandler) Handle(payload *router.Payload, response *router.Response) {
	payload.Req.ParseForm()

	store := h.AssetStore
	fileName := clean(payload.Params[0])
	if store.(skyAsset.URLSigner).IsSignatureRequired() {
		expiredAtUnix, err := strconv.ParseInt(payload.Req.Form.Get("expiredAt"), 10, 64)
		if err != nil {
			response.Err = skyerr.NewError(skyerr.InvalidArgument, "expect expiredAt to be an integer")
			return
		}

		signature := payload.Req.Form.Get("signature")
		requestErr := validateAssetGetRequest(h.AssetStore, fileName, expiredAtUnix, signature)
		if requestErr != nil {
			response.Err = requestErr
			return
		}
	}

	// everything's right, proceed with the request

	conn := payload.DBConn
	asset := skydb.Asset{}
	if err := conn.GetAsset(fileName, &asset); err != nil {
		log.Errorf("Failed to get asset: %v", err)

		response.Err = skyerr.NewResourceFetchFailureErr("asset", fileName)
		return
	}

	response.Header().Set("Content-Type", asset.ContentType)
	response.Header().Set("Content-Length", strconv.FormatInt(asset.Size, 10))

	reader, err := store.GetFileReader(fileName)
	if err != nil {
		log.Errorf("Failed to get file reader: %v", err)

		response.Err = skyerr.NewResourceFetchFailureErr("asset", fileName)
		return
	}
	defer reader.Close()

	if _, err := io.Copy(response, reader); err != nil {
		// there is nothing we can do if error occurred after started
		// writing a response. Log.
		log.Errorf("Error writing file to response: %v", err)
	}
}

// AssetUploadURLHandler receives and persists a file to be associated by Record.
//
// Example curl:
//	curl -XPUT \
//		-H 'X-Skygear-API-Key: apiKey' \
//		-H 'Content-Type: text/plain' \
//		--data-binary '@file.txt' \
//		http://localhost:3000/files/filename
type AssetUploadURLHandler struct {
	AssetStore skyAsset.Store `inject:"AssetStore"`
}

func (h *AssetUploadURLHandler) Setup() {
	return
}

func (h *AssetUploadURLHandler) GetPreprocessors() []router.Processor {
	return nil
}

func (h *AssetUploadURLHandler) Handle(payload *router.Payload, response *router.Response) {
	var (
		fileName, contentType string
	)

	fileName = clean(payload.Params[0])

	dir, file := filepath.Split(fileName)
	file = fmt.Sprintf("%s-%s", uuidNew(), file)

	fileName = filepath.Join(dir, file)
	contentType = payload.Req.Header.Get("Content-Type")

	if contentType == "" {
		response.Err = skyerr.NewError(skyerr.InvalidArgument, "Content-Type cannot be empty")
		return
	}

	written, tempFile, err := copyToTempFile(payload.Req.Body)
	if err != nil {
		response.Err = skyerr.NewUnknownErr(err)
		return
	}
	defer func() {
		tempFile.Close()
		os.Remove(tempFile.Name())
	}()

	if written == 0 {
		response.Err = skyerr.NewError(skyerr.InvalidArgument, "Zero-byte content")
		return
	}

	assetStore := h.AssetStore
	if err := assetStore.PutFileReader(fileName, tempFile, written, contentType); err != nil {
		response.Err = skyerr.NewUnknownErr(err)
		return
	}

	asset := skydb.Asset{
		Name:        fileName,
		ContentType: contentType,
		Size:        written,
	}

	conn := payload.DBConn
	if err := conn.SaveAsset(&asset); err != nil {
		response.Err = skyerr.NewResourceSaveFailureErrWithStringID("asset", asset.Name)
		return
	}

	if signer, ok := h.AssetStore.(skyAsset.URLSigner); ok {
		asset.Signer = signer
	} else {
		log.Warnf("Failed to acquire asset URLSigner, please check configuration")
		response.Err = skyerr.NewError(skyerr.UnexpectedError, "Failed to sign the url")
		return
	}
	response.Result = skydbconv.ToMap((*skydbconv.MapAsset)(&asset))
}

func copyToTempFile(src io.Reader) (written int64, tempFile *os.File, err error) {
	tempFile, err = ioutil.TempFile("", "")
	if err != nil {
		return
	}
	written, err = io.Copy(tempFile, src)
	if err != nil {
		cleanupFile(tempFile)
		tempFile = nil
		return
	}
	if _, err = tempFile.Seek(0, 0); err != nil {
		cleanupFile(tempFile)
		tempFile = nil
		return
	}
	return
}

func cleanupFile(f *os.File) error {
	closeErr := f.Close()
	if closeErr != nil {
		log.Errorf("Failed to close tempFile %s: %v", f.Name(), closeErr)
		return closeErr
	}

	if err := os.Remove(f.Name()); err != nil {
		log.Errorf("Failed to remove file %s: %v", f.Name(), err)
		return err
	}

	return nil
}
