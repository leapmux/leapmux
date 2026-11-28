package control

// SetPinsForTest injects a test PinStore into c without exposing the pins field
// on the production API.
func SetPinsForTest(c *HubClient, pins *PinStore) {
	c.pins = pins
}
