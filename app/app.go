package app

import (
	"encoding/json"
	"fmt"
	"log"

	contract "github.com/slidebolt/sb-contract"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
	"github.com/slidebolt/sb-virtual/virtual"
)

type App struct {
	msg   messenger.Messenger
	store storage.Storage
	sub   messenger.Subscription
}

func New() *App {
	return &App{}
}

func (a *App) Hello() contract.HelloResponse {
	return contract.HelloResponse{
		ID:              "virtual",
		Kind:            contract.KindService,
		ContractVersion: contract.ContractVersion,
		DependsOn:       []string{"messenger", "storage"},
	}
}

func (a *App) OnStart(deps map[string]json.RawMessage) (json.RawMessage, error) {
	msg, err := messenger.Connect(deps)
	if err != nil {
		return nil, fmt.Errorf("virtual: connect messenger: %w", err)
	}
	a.msg = msg

	store, err := storage.Connect(deps)
	if err != nil {
		return nil, fmt.Errorf("virtual: connect storage: %w", err)
	}
	a.store = store

	h := virtual.NewHandler(msg, store)
	sub, err := h.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("virtual: subscribe: %w", err)
	}
	a.sub = sub

	log.Println("virtual: ready, listening on *.*.*.command.>")
	return nil, nil
}

func (a *App) OnShutdown() error {
	if a.sub != nil {
		a.sub.Unsubscribe()
	}
	if a.store != nil {
		a.store.Close()
	}
	if a.msg != nil {
		a.msg.Close()
	}
	return nil
}
