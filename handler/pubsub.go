package handler

import (
	"github.com/oursky/skygear/pubsub"
	"github.com/oursky/skygear/router"
)

type PubSubHandler struct {
	WebSocket *pubsub.WsPubSub
}

func (h *PubSubHandler) Setup() {
	return
}

func (h *PubSubHandler) GetPreprocessors() []router.Processor {
	return nil
}

func (h *PubSubHandler) Handle(payload *router.Payload, response *router.Response) {
	h.WebSocket.Handle(response, payload.Req)
}
