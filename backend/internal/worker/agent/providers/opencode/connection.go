package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const openCodeNativeVersion = "2.2.1"

// openCodeServiceRegistration is the private canonical service advertisement.
// Never format the whole value: Password is a service credential.
type openCodeServiceRegistration struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	URL      string `json:"url"`
	PID      int    `json:"pid"`
	Password string `json:"password"`
}

func decodeOpenCodeServiceRegistration(raw []byte) (openCodeServiceRegistration, error) {
	var registration openCodeServiceRegistration
	if err := json.Unmarshal(raw, &registration); err != nil {
		return openCodeServiceRegistration{}, errors.New("the OpenCode service registration is not valid JSON")
	}
	if strings.TrimSpace(registration.ID) == "" || registration.PID <= 0 || registration.Password == "" {
		return openCodeServiceRegistration{}, errors.New("the OpenCode service registration has no complete owner identity")
	}
	if registration.Version != openCodeNativeVersion {
		return openCodeServiceRegistration{}, fmt.Errorf("the OpenCode service requires version %s", openCodeNativeVersion)
	}
	endpoint, err := url.Parse(registration.URL)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return openCodeServiceRegistration{}, errors.New("the OpenCode service registration has an invalid HTTP address")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return openCodeServiceRegistration{}, errors.New("the OpenCode service registration has an invalid port")
	}
	return registration, nil
}

func (registration openCodeServiceRegistration) sameOwner(other openCodeServiceRegistration) bool {
	return registration.ID == other.ID && registration.Version == other.Version && registration.URL == other.URL && registration.PID == other.PID && registration.Password == other.Password
}
