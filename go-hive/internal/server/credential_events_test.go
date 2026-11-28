package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// credentialEventGrabber subscribes to the process-global bus the credential
// routes publish on and hands back events in order.
func credentialEventGrabber(t *testing.T) (chan *eventbus.AgentEvent, func()) {
	t.Helper()
	events := make(chan *eventbus.AgentEvent, 16)
	bus := eventbus.GetGlobalEventBus()
	subID := bus.Subscribe([]eventbus.EventType{
		eventbus.EventCredentialProviderConnected,
		eventbus.EventCredentialProviderDisconnected,
		eventbus.EventToolCatalogRefreshed,
	}, func(event *eventbus.AgentEvent) {
		select {
		case events <- event:
		default:
		}
	})
	return events, func() { bus.Unsubscribe(subID) }
}

func nextCredentialEvent(t *testing.T, events chan *eventbus.AgentEvent) *eventbus.AgentEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a credential event")
		return nil
	}
}

func assertNoCredentialEvent(t *testing.T, events chan *eventbus.AgentEvent) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected extra event: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

// Parity: framework/server/routes_credentials.py::_publish_credential_event
//
// Negative regression: the app assembly must wire the production publisher.
// Without it the events were type-defined and allowlisted but never emitted,
// so this test would time out waiting for the connected event.
func TestAppPublishesCredentialCatalogEvents(t *testing.T) {
	t.Setenv("NOTION_API_KEY", "")
	home := hivehome.Home{Root: t.TempDir()}
	app := newAppForTest(t, AppDeps{Home: home})
	events, stop := credentialEventGrabber(t)
	defer stop()

	recorder, body := doWiringJSON(t, app, http.MethodPost, "/api/credentials",
		`{"credential_id":"notion","keys":{"api_key":"x"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("save status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["saved"] != "notion" {
		t.Fatalf("save body = %#v", body)
	}

	connected := nextCredentialEvent(t, events)
	if connected.Type != eventbus.EventCredentialProviderConnected || connected.StreamID != "global" {
		t.Fatalf("connected event = %#v", connected)
	}
	if connected.Data["credential_id"] != "notion" || connected.Data["provider"] != "notion" {
		t.Fatalf("connected data = %#v", connected.Data)
	}
	refreshed := nextCredentialEvent(t, events)
	if refreshed.Type != eventbus.EventToolCatalogRefreshed || refreshed.StreamID != "global" {
		t.Fatalf("refreshed event = %#v", refreshed)
	}
	if refreshed.Data["trigger"] != "credential_save" || refreshed.Data["credential_id"] != "notion" || refreshed.Data["provider"] != "notion" {
		t.Fatalf("refreshed data = %#v", refreshed.Data)
	}

	recorder, _ = doWiringJSON(t, app, http.MethodDelete, "/api/credentials/notion", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	disconnected := nextCredentialEvent(t, events)
	if disconnected.Type != eventbus.EventCredentialProviderDisconnected || disconnected.Data["credential_id"] != "notion" {
		t.Fatalf("disconnected event = %#v", disconnected)
	}
	refreshed = nextCredentialEvent(t, events)
	if refreshed.Type != eventbus.EventToolCatalogRefreshed || refreshed.Data["trigger"] != "credential_delete" {
		t.Fatalf("delete refresh = %#v", refreshed)
	}
	assertNoCredentialEvent(t, events)
}

// Parity: framework/server/routes_credentials.py::_provider_for_credential
//
// A credential with no OAuth provider publishes provider=null (upstream's
// None), not an empty string.
func TestCredentialCatalogEventProviderIsNullWhenUnknown(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	app := newAppForTest(t, AppDeps{Home: home})
	events, stop := credentialEventGrabber(t)
	defer stop()

	recorder, _ := doWiringJSON(t, app, http.MethodPost, "/api/credentials",
		`{"credential_id":"widgetco","keys":{"api_key":"x"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("save status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	connected := nextCredentialEvent(t, events)
	if connected.Type != eventbus.EventCredentialProviderConnected || connected.Data["credential_id"] != "widgetco" {
		t.Fatalf("connected event = %#v", connected)
	}
	if value, present := connected.Data["provider"]; !present || value != nil {
		t.Fatalf("provider = %#v, present = %v; want null", value, present)
	}
	refreshed := nextCredentialEvent(t, events)
	if refreshed.Type != eventbus.EventToolCatalogRefreshed || refreshed.Data["provider"] != nil {
		t.Fatalf("refreshed event = %#v", refreshed)
	}
	assertNoCredentialEvent(t, events)
}

// Parity: framework/server/routes_credentials.py::handle_save_local_credential
//
// The local-account add/remove pair is a separate route table and needs the
// same publisher wiring; an unwired pair leaves the Integrations page stale.
func TestLocalCredentialRoutesPublishCatalogEvents(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	app := newAppForTest(t, AppDeps{Home: home})
	events, stop := credentialEventGrabber(t)
	defer stop()

	recorder, _ := doWiringJSON(t, app, http.MethodPost, "/api/credentials/local",
		`{"credential_id":"widgetco","account":"work","keys":{"api_key":"x"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("local save status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	connected := nextCredentialEvent(t, events)
	if connected.Type != eventbus.EventCredentialProviderConnected || connected.Data["credential_id"] != "widgetco" || connected.Data["provider"] != nil {
		t.Fatalf("local connected event = %#v", connected)
	}
	refreshed := nextCredentialEvent(t, events)
	if refreshed.Type != eventbus.EventToolCatalogRefreshed || refreshed.Data["trigger"] != "credential_save" {
		t.Fatalf("local save refresh = %#v", refreshed)
	}

	recorder, _ = doWiringJSON(t, app, http.MethodDelete, "/api/credentials/local/widgetco/work", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("local delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	disconnected := nextCredentialEvent(t, events)
	if disconnected.Type != eventbus.EventCredentialProviderDisconnected || disconnected.Data["credential_id"] != "widgetco" {
		t.Fatalf("local disconnected event = %#v", disconnected)
	}
	refreshed = nextCredentialEvent(t, events)
	if refreshed.Type != eventbus.EventToolCatalogRefreshed || refreshed.Data["trigger"] != "credential_delete" {
		t.Fatalf("local delete refresh = %#v", refreshed)
	}
}
