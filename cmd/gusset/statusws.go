package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/justinstimatze/gusset/internal/config"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/status"
	"github.com/justinstimatze/gusset/internal/statusws"
)

// startStatusWS brings up the localhost status WebSocket for the run, bound to
// addr and tied to ctx. It blocks only until the listener is up (or fails fast
// on a bad/non-loopback address), then leaves the server running in the
// background. The pairing token is not printed here — `gusset ws-token` prints
// it on demand, so the secret stays out of every run's scrollback.
func startStatusWS(ctx context.Context, addr string, model *status.Model, k *crypto.Keys) error {
	token, err := statusws.Token(k)
	if err != nil {
		return err
	}
	srv := statusws.NewServer(model, token)

	ready := make(chan net.Addr, 1)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ctx, addr, func(a net.Addr) { ready <- a }) }()

	select {
	case a := <-ready:
		fmt.Printf("status: serving live status at ws://%s — pair the extension with `gusset ws-token`\n", a)
		return nil
	case err := <-errc:
		return fmt.Errorf("status WebSocket: %w", err)
	case <-time.After(3 * time.Second):
		return errors.New("status WebSocket: listener did not come up in time")
	}
}

// wsTokenCmd prints the localhost-WebSocket pairing token derived from the
// passphrase. The user copies it into the companion extension once; the daemon
// derives the same token, so no key exchange is needed.
func wsTokenCmd(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pass, err := readPassphrase(cfg)
	if err != nil {
		return err
	}
	if err := crypto.ValidatePassphrase(pass); err != nil {
		return err
	}
	k, err := crypto.DeriveKeys(pass, cfg.SaltOrApp())
	if err != nil {
		return err
	}
	token, err := statusws.Token(k)
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}
