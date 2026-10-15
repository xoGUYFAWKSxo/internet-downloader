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

// Detail is what a download's progress window shows.
type Detail struct {
	Status Status `json:"status"`
	Host   string `json:"host,omitempty"` // the server; never the full address, which can hold tokens
	// Resume is "yes" or "no" once the server has been asked whether the
	// download can carry on after a stop, else "".
	Resume string `json:"resume,omitempty"`
	Have   []Span `json:"have"`  // the parts of the file written so far
	Conns  []Conn `json:"conns"` // the streams, while it runs
}

// Span is the bytes from Start up to End.
type Span struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// AskInfo is what the Download File Info box shows.
type AskInfo struct {
	ID       uint64 `json:"id"`
	Host     string `json:"host"` // never the full address, which can hold tokens
	Name     string `json:"name"`
	Size     int64  `json:"size"` // -1 while unknown
	Checking bool   `json:"checking"`
	// Resume is "yes" or "no" once the server has answered.
	Resume string `json:"resume"`
	// Stream is an HLS or DASH stream: its file type is picked when it is
	// joined, and it can't wait in the list, as it can't be resumed.
	Stream bool   `json:"stream"`
	Error  string `json:"error,omitempty"`
	Dir    string `json:"dir"` // the folder offered
}
