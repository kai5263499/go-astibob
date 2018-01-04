package astibob

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"text/template"

	"github.com/asticode/go-astibob/brain"
	"github.com/asticode/go-astilog"
	"github.com/asticode/go-astitools/http"
	"github.com/asticode/go-astiws"
	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"github.com/pkg/errors"
)

// Clients websocket events
const (
	clientsWebsocketEventNameAbilityStart      = "ability.start"
	clientsWebsocketEventNameAbilityStarted    = "ability.started"
	clientsWebsocketEventNameAbilityStop       = "ability.stop"
	clientsWebsocketEventNameAbilityStopped    = "ability.stopped"
	clientsWebsocketEventNameBrainRegistered   = "brain.registered"
	clientsWebsocketEventNameBrainDisconnected = "brain.disconnected"
	clientsWebsocketEventNamePing              = "ping"
)

// clientsServer is a server for the clients
type clientsServer struct {
	*server
	brains   *brains
	stopFunc func()
}

// newClientsServer creates a new clients server.
func newClientsServer(t map[string]*template.Template, brains *brains, clientsWs *astiws.Manager, stopFunc func(), o Options) (s *clientsServer) {
	// Create server
	s = &clientsServer{
		brains:   brains,
		server:   newServer("clients", clientsWs, o.ClientsServer),
		stopFunc: stopFunc,
	}

	// Init router
	var r = httprouter.New()

	// Static files
	r.ServeFiles("/static/*filepath", http.Dir(filepath.Join(o.ResourcesDirectory, "static")))

	// Web
	r.GET("/", s.handleHomepageGET)
	r.GET("/web/*page", s.handleWebGET(t))

	// Websockets
	r.GET("/websocket", s.handleWebsocketGET)

	// API
	r.GET("/api/bob", s.handleAPIBobGET)
	r.GET("/api/bob/stop", s.handleAPIBobStopGET)
	r.GET("/api/ok", s.handleAPIOKGET)
	r.GET("/api/references", s.handleAPIReferencesGET)

	// Chain middlewares
	var h = astihttp.ChainMiddlewares(r, astihttp.MiddlewareBasicAuth(o.ClientsServer.Username, o.ClientsServer.Password))
	h = astihttp.ChainMiddlewaresWithPrefix(h, []string{"/web/", "/api/"}, astihttp.MiddlewareTimeout(o.ClientsServer.Timeout))
	h = astihttp.ChainMiddlewaresWithPrefix(h, []string{"/web/"}, astihttp.MiddlewareContentType("text/html; charset=UTF-8"))
	h = astihttp.ChainMiddlewaresWithPrefix(h, []string{"/api/"}, astihttp.MiddlewareContentType("application/json"))

	// Set handler
	s.setHandler(h)
	return
}

// handleHomepageGET handles the homepage.
func (s *clientsServer) handleHomepageGET(rw http.ResponseWriter, r *http.Request, p httprouter.Params) {
	http.Redirect(rw, r, "/web/index", http.StatusPermanentRedirect)
}

// handleWebGET handles the Web pages.
func (s *clientsServer) handleWebGET(t map[string]*template.Template) httprouter.Handle {
	return func(rw http.ResponseWriter, r *http.Request, p httprouter.Params) {
		// Check if template exists
		var name = p.ByName("page") + ".html"
		if _, ok := t[name]; !ok {
			name = "/errors/404.html"
		}

		// Get data
		var code = http.StatusOK
		var data interface{}
		data = s.templateData(r, p, &name, &code)

		// Write header
		rw.WriteHeader(code)

		// Execute template
		if err := t[name].Execute(rw, data); err != nil {
			astilog.Error(errors.Wrapf(err, "astibob: executing %s template with data %#v failed", name, data))
			return
		}
	}
}

// templateData returns a template data.
func (s *clientsServer) templateData(r *http.Request, p httprouter.Params, name *string, code *int) (data interface{}) {
	// Switch on name
	switch *name {
	case "/errors/404.html":
		*code = http.StatusNotFound
	case "/index.html":
	}
	return
}

// handleWebsocketGET handles the websockets.
func (s *clientsServer) handleWebsocketGET(rw http.ResponseWriter, r *http.Request, p httprouter.Params) {
	if err := s.ws.ServeHTTP(rw, r, s.adaptWebsocketClient); err != nil {
		if v, ok := errors.Cause(err).(*websocket.CloseError); !ok || (v.Code != websocket.CloseNoStatusReceived && v.Code != websocket.CloseNormalClosure) {
			astilog.Error(errors.Wrapf(err, "astibob: handling websocket on %s failed", s.s.Addr))
		}
		return
	}
}

// ClientAdapter returns the client adapter.
func (s *clientsServer) adaptWebsocketClient(c *astiws.Client) {
	s.ws.AutoRegisterClient(c)
	c.AddListener(astiws.EventNameDisconnect, s.handleWebsocketDisconnected)
	c.AddListener(clientsWebsocketEventNameAbilityStart, s.handleWebsocketAbilityToggle)
	c.AddListener(clientsWebsocketEventNameAbilityStop, s.handleWebsocketAbilityToggle)
	c.AddListener(clientsWebsocketEventNamePing, s.handleWebsocketPing)
}

// handleWebsocketDisconnected handles the disconnected websocket event
func (s *clientsServer) handleWebsocketDisconnected(c *astiws.Client, eventName string, payload json.RawMessage) error {
	s.ws.UnregisterClient(c)
	return nil
}

// handleWebsocketPing handles the ping websocket event
func (s *clientsServer) handleWebsocketPing(c *astiws.Client, eventName string, payload json.RawMessage) error {
	if err := c.HandlePing(); err != nil {
		astilog.Error(errors.Wrap(err, "handling ping failed"))
	}
	return nil
}

// handleWebsocketAbilityToggle handles the ability toggle websocket events
func (s *clientsServer) handleWebsocketAbilityToggle(c *astiws.Client, eventName string, payload json.RawMessage) error {
	// Decode payload
	var e EventAbility
	if err := json.Unmarshal(payload, &e); err != nil {
		astilog.Error(errors.Wrapf(err, "astibob: json unmarshaling %s payload %#v failed", eventName, payload))
		return nil
	}

	// Retrieve brain
	b, ok := s.brains.brain(e.BrainName)
	if !ok {
		astilog.Error(fmt.Errorf("astibob: unknown brain %s", e.BrainName))
		return nil
	}

	// Retrieve ability
	_, ok = b.ability(e.Name)
	if !ok {
		astilog.Error(fmt.Errorf("astibob: unknown ability %s for brain %s", e.Name, b.name))
		return nil
	}

	// Get event name
	var eventNameBrain = astibrain.WebsocketEventNameAbilityStop
	if eventName == clientsWebsocketEventNameAbilityStart {
		eventNameBrain = astibrain.WebsocketEventNameAbilityStart
	}

	// Dispatch to brain
	dispatchWsEventToClient(b.ws, eventNameBrain, e.Name)
	return nil
}

// APIError represents an API error.
type APIError struct {
	Message string `json:"message"`
}

// apiWriteError writes an API error
func apiWriteError(rw http.ResponseWriter, code int, err error) {
	rw.WriteHeader(code)
	astilog.Error(err)
	if err := json.NewEncoder(rw).Encode(APIError{Message: err.Error()}); err != nil {
		astilog.Error(errors.Wrap(err, "astibob: json encoding failed"))
	}
}

// apiWrite writes API data
func apiWrite(rw http.ResponseWriter, data interface{}) {
	if err := json.NewEncoder(rw).Encode(data); err != nil {
		apiWriteError(rw, http.StatusInternalServerError, errors.Wrap(err, "astibob: json encoding failed"))
		return
	}
}

// handleAPIBobGET returns Bob's information.
func (s *clientsServer) handleAPIBobGET(rw http.ResponseWriter, r *http.Request, p httprouter.Params) {
	apiWrite(rw, newEventBob(s.brains))
}

// handleAPIBobStopGET stops Bob.
func (s *clientsServer) handleAPIBobStopGET(rw http.ResponseWriter, r *http.Request, p httprouter.Params) {
	s.stopFunc()
	rw.WriteHeader(http.StatusNoContent)
}

// handleAPIOKGET returns the ok status.
func (s *clientsServer) handleAPIOKGET(rw http.ResponseWriter, r *http.Request, p httprouter.Params) {
	rw.WriteHeader(http.StatusNoContent)
}

// APIReferences represents the references.
type APIReferences struct {
	WsURL        string `json:"ws_url"`
	WsPingPeriod int    `json:"ws_ping_period"` // In seconds
}

// handleAPIReferencesGET returns the references.
func (s *clientsServer) handleAPIReferencesGET(rw http.ResponseWriter, r *http.Request, p httprouter.Params) {
	apiWrite(rw, APIReferences{
		WsURL:        "ws://" + s.o.PublicAddr + "/websocket",
		WsPingPeriod: int(astiws.PingPeriod.Seconds()),
	})
}