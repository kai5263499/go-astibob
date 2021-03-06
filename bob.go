package astibob

import (
	"context"
	"path/filepath"

	"regexp"
	"strings"

	"github.com/asticode/go-astilog"
	"github.com/asticode/go-astitools/template"
	"github.com/asticode/go-astiws"
	"github.com/pkg/errors"
)

// Bob is an object handling a collection of brains.
type Bob struct {
	brains        *brains
	brainsServer  *brainsServer
	c             Configuration
	cancel        context.CancelFunc
	clientsServer *clientsServer
	ctx           context.Context
	dispatcher    *dispatcher
	interfaces    *interfaces
	templater     *astitemplate.Templater
}

// Configuration represents a Bob configuration.
type Configuration struct {
	BrainsServer       ServerConfiguration `toml:"brains_server"`
	ClientsServer      ServerConfiguration `toml:"clients_server"`
	ResourcesDirectory string              `toml:"resources_directory"`
}

// New creates a new Bob.
func New(c Configuration) (b *Bob, err error) {
	// Create bob
	b = &Bob{
		brains:     newBrains(),
		c:          c,
		dispatcher: newDispatcher(),
		interfaces: newInterfaces(),
	}

	// Create templater
	if b.templater, err = astitemplate.NewTemplater(filepath.Join(b.c.ResourcesDirectory, "templates", "pages"), filepath.Join(b.c.ResourcesDirectory, "templates", "layouts"), ".html"); err != nil {
		err = errors.Wrapf(err, "astibob: creating templater with resources directory %s failed", b.c.ResourcesDirectory)
		return
	}

	// Create servers
	brainsWs := astiws.NewManager(c.BrainsServer.Ws)
	clientsWs := astiws.NewManager(c.BrainsServer.Ws)
	b.brainsServer = newBrainsServer(b.templater, b.brains, brainsWs, clientsWs, b.dispatcher, b.interfaces, c.BrainsServer)
	b.clientsServer = newClientsServer(b.templater, b.brains, clientsWs, b.interfaces, b.stop, c)
	return
}

// Close implements the io.Closer interface.
func (b *Bob) Close() (err error) {
	// Close brains server
	astilog.Debug("astibob: closing brains server")
	if err = b.brainsServer.Close(); err != nil {
		astilog.Error(errors.Wrap(err, "astibob: closing brains server failed"))
	}

	// Close clients server
	astilog.Debug("astibob: closing clients server")
	if err = b.clientsServer.Close(); err != nil {
		astilog.Error(errors.Wrap(err, "astibob: closing clients server failed"))
	}
	return
}

// Declare declares an ability interface
func (b *Bob) Declare(i Interface) {
	b.interfaces.set(i)
}

// Run runs Bob.
// This is cancellable through the ctx.
func (b *Bob) Run(ctx context.Context) (err error) {
	// Reset ctx
	b.ctx, b.cancel = context.WithCancel(ctx)
	defer b.cancel()

	// Run brains server
	var chanDone = make(chan error)
	go func() {
		if err := b.brainsServer.run(); err != nil {
			chanDone <- err
		}
	}()
	go func() {
		if err := b.clientsServer.run(); err != nil {
			chanDone <- err
		}
	}()

	// Dispatch event
	// TODO Only fire this event once servers are up and running
	b.dispatcher.dispatch(Event{Name: EventNameReady})

	// Wait for context or chanDone to be done
	select {
	case <-b.ctx.Done():
		if b.ctx.Err() != context.Canceled {
			err = errors.Wrap(err, "astibob: context error")
		}
		return
	case err = <-chanDone:
		if err != nil {
			err = errors.Wrap(err, "astibob: running servers failed")
		}
		return
	}
}

// stop stops Bob
func (b *Bob) stop() {
	b.cancel()
}

// dispatchWsEventToManager dispatches a websocket event to a manager.
func dispatchWsEventToManager(ws *astiws.Manager, name string, payload interface{}) {
	ws.Loop(func(k interface{}, c *astiws.Client) {
		dispatchWsEventToClient(c, name, payload)
	})
}

// dispatchWsEventToClient dispatches a websocket event to a client.
func dispatchWsEventToClient(c *astiws.Client, name string, payload interface{}) {
	if err := c.Write(name, payload); err != nil {
		astilog.Error(errors.Wrapf(err, "astibob: writing %s event with payload %#v to ws client %p failed", name, payload, c))
		return
	}
}

// On adds a listener to an event
func (b *Bob) On(eventName string, l Listener) {
	b.dispatcher.addListener(eventName, l)
}

// regexpKey represents the key regexp
var regexpKey = regexp.MustCompile("[^\\w]+")

// key creates a key based on a name
func key(name string) string {
	return regexpKey.ReplaceAllString(strings.ToLower(name), "-")
}
