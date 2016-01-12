package plugin

import (
	"encoding/json"

	log "github.com/Sirupsen/logrus"

	"github.com/oursky/skygear/router"
	"github.com/oursky/skygear/skyerr"
)

type LambdaHandler struct {
	Plugin            *Plugin
	Name              string
	AccessKeyRequired bool
	UserRequired      bool
}

func (h *LambdaHandler) Setup() {
	return
}

func (h *LambdaHandler) GetPreprocessors() []router.Processor {
	return nil
}

// Handle executes lambda function implemented by the plugin.
func (h *LambdaHandler) Handle(payload *router.Payload, response *router.Response) {
	inbytes, err := json.Marshal(payload.Data)
	if err != nil {
		response.Err = skyerr.NewUnknownErr(err)
		return
	}

	outbytes, err := h.Plugin.transport.RunLambda(payload.Context, h.Name, inbytes)
	if err != nil {
		response.Err = skyerr.NewUnknownErr(err)
		return
	}

	result := map[string]interface{}{}
	err = json.Unmarshal(outbytes, &result)
	if err != nil {
		response.Err = skyerr.NewUnknownErr(err)
		return
	}
	log.WithFields(log.Fields{
		"name":   h.Name,
		"input":  payload.Data,
		"result": result,
		"err":    err,
	}).Debugf("Executed a lambda with result")

	response.Result = result
}
