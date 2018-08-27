package metrics

import (
	"encoding/json"
	"io"
	"time"
)

func (r *StandardRegistry) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.GetAll())
}

func WriteJSON(r Registry, d time.Duration, w io.Writer) {
	for range time.Tick(d) {
		WriteJSONOnce(r, w)
	}
}

func WriteJSONOnce(r Registry, w io.Writer) {
	json.NewEncoder(w).Encode(r)
}

func (p *PrefixedRegistry) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.GetAll())
}
