package fetch

// States of a download.
const (
	Checking    = "checking"
	Downloading = "downloading"
	Joining     = "joining" // FFmpeg is joining the tracks of a stream
	Waiting     = "waiting" // the connection dropped; trying again soon
	Paused      = "paused"  // stopped with the app; can be resumed
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
	// Segments of an HLS or DASH stream, when it is one.
	Parts     int    `json:"parts,omitempty"`
	PartsDone int    `json:"partsDone,omitempty"`
	Started   string `json:"started"`
	// Resumable means the unfinished file is kept and Resume picks it up.
	Resumable bool `json:"resumable,omitempty"`
}
