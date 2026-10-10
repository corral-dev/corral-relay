package hub

import "encoding/json"

func registeredPayload(host *Host) []byte {
	body, _ := json.Marshal(map[string]any{
		"generation":   host.Generation,
		"node":         host.NodeID,
		"capabilities": []string{"host_lane_attach"},
	})
	return body
}

func laneAttachedPayload(lane string, generation uint64) []byte {
	body, _ := json.Marshal(map[string]any{
		"lane":       lane,
		"generation": generation,
	})
	return body
}
