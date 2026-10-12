package fetch

// States of a download.
const (
	Checking    = "checking"
	Downloading = "downloading"
	Done        = "done"
	Failed      = "failed"
	Cancelled   = "cancelled"
)

// Status is what the window sees of one download. Done counts bytes
// actually written, so it is a measured number.
type Status struct {
	ID       uint64 `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"` // -1 when unknown
	Done     int64  `json:"done"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
	Path     string `json:"path,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Streams  int    `json:"streams,omitempty"`
	Started  string `json:"started"`
}
