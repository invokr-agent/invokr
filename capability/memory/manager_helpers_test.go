package memory

import (
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"

	"go.uber.org/zap"
)

func NewManagerFromConfigWithEvents(app config.AppConfig, dispatcher events.Dispatcher, logger ...*zap.Logger) (Manager, error) {
	return newManagerFromConfigWithEvents(app, dispatcher, nil, nil, logger...)
}

func NewManagerFromConfigWithWorker(app config.AppConfig, dispatcher events.Dispatcher, worker memoryWorker, logger ...*zap.Logger) (Manager, error) {
	return newManagerFromConfigWithEvents(app, dispatcher, worker, nil, logger...)
}
