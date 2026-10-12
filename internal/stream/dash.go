package stream

import (
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
)

// maxSegments guards against a manifest that would expand to millions of
// segments (a wrong duration or a tiny segment length).
const maxSegments = 200000

type mpd struct {
	Type     string   `xml:"type,attr"`
	Duration string   `xml:"mediaPresentationDuration,attr"`
	BaseURL  []string `xml:"BaseURL"`
	Periods  []struct {
		Duration string       `xml:"duration,attr"`
		BaseURL  []string     `xml:"BaseURL"`
		Sets     []adaptation `xml:"AdaptationSet"`
		Template *segTemplate `xml:"SegmentTemplate"`
		List     *segList     `xml:"SegmentList"`
		Protect  []xml.Name   `xml:"ContentProtection"`
	} `xml:"Period"`
}

type adaptation struct {
	ContentType string       `xml:"contentType,attr"`
	MimeType    string       `xml:"mimeType,attr"`
	Codecs      string       `xml:"codecs,attr"`
	BaseURL     []string     `xml:"BaseURL"`
	Protect     []xml.Name   `xml:"ContentProtection"`
	Template    *segTemplate `xml:"SegmentTemplate"`
	List        *segList     `xml:"SegmentList"`
	Roles       []struct {
		V string `xml:"value,attr"`
	} `xml:"Role"`
	Reps []representation `xml:"Representation"`
}

type representation struct {
	ID        string       `xml:"id,attr"`
	Bandwidth int          `xml:"bandwidth,attr"`
	Width     int          `xml:"width,attr"`
	Height    int          `xml:"height,attr"`
	MimeType  string       `xml:"mimeType,attr"`
	Codecs    string       `xml:"codecs,attr"`
	BaseURL   []string     `xml:"BaseURL"`
	Protect   []xml.Name   `xml:"ContentProtection"`
	Template  *segTemplate `xml:"SegmentTemplate"`
	List      *segList     `xml:"SegmentList"`
}

type segTemplate struct {
	Media     string `xml:"media,attr"`
	Init      string `xml:"initialization,attr"`
	Start     string `xml:"startNumber,attr"`
	Timescale string `xml:"timescale,attr"`
	Duration  string `xml:"duration,attr"`
	Timeline  *struct {
		S []struct {
			T string `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int64  `xml:"r,attr"`
		} `xml:"S"`
	} `xml:"SegmentTimeline"`
}

type segList struct {
	Init *struct {
		URL   string `xml:"sourceURL,attr"`
		Range string `xml:"range,attr"`
	} `xml:"Initialization"`
	URLs []struct {
		Media string `xml:"media,attr"`
		Range string `xml:"mediaRange,attr"`
	} `xml:"SegmentURL"`
}

// merge fills t's empty fields from outer (the AdaptationSet's or Period's
// template), the way DASH inherits them.
func (t *segTemplate) merge(outer *segTemplate) *segTemplate {
	if t == nil {
		return outer
	}
	if outer == nil {
		return t
	}
	m := *t
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&m.Media, outer.Media}, {&m.Init, outer.Init}, {&m.Start, outer.Start},
		{&m.Timescale, outer.Timescale}, {&m.Duration, outer.Duration},
	} {
		if *f.dst == "" {
			*f.dst = f.src
		}
	}
	if m.Timeline == nil {
		m.Timeline = outer.Timeline
	}
	return &m
}

func resolveDASH(u, text string) (*Plan, error) {
	var m mpd
	if err := xml.Unmarshal([]byte(text), &m); err != nil {
		return nil, fmt.Errorf("the DASH manifest can't be read: %w", err)
	}
	if m.Type == "dynamic" {
		return nil, ErrLive
	}
	if len(m.Periods) == 0 {
		return nil, errors.New("the DASH manifest has no content")
	}
	if len(m.Periods) > 1 {
		return nil, errors.New("this DASH stream is split into several periods (often for adverts), which isn't supported yet")
	}
	p := m.Periods[0]
	total, err := isoDuration(p.Duration)
	if err != nil || total == 0 {
		total, err = isoDuration(m.Duration)
	}
	if err != nil {
		return nil, err
	}
	base, err := chain(u, m.BaseURL, p.BaseURL)
	if err != nil {
		return nil, err
	}
	type pick struct {
		set *adaptation
		rep *representation
	}
	var best [2]*pick // video, audio
	for i := range p.Sets {
		set := &p.Sets[i]
		if len(p.Protect) > 0 || len(set.Protect) > 0 {
			return nil, media.ErrDRM
		}
		for j := range set.Reps {
			rep := &set.Reps[j]
			if len(rep.Protect) > 0 {
				return nil, media.ErrDRM
			}
			k := -1
			switch contentType(set, rep) {
			case "video":
				k = 0
			case "audio":
				if !secondaryAudio(set) {
					k = 1
				}
			}
			if k >= 0 && (best[k] == nil || rep.Bandwidth > best[k].rep.Bandwidth) {
				best[k] = &pick{set, rep}
			}
		}
	}
	plan := &Plan{}
	for k, b := range best {
		if b == nil {
			continue
		}
		rbase, err := chain(base, b.set.BaseURL, b.rep.BaseURL)
		if err != nil {
			return nil, err
		}
		tmpl := b.rep.Template.merge(b.set.Template).merge(p.Template)
		list := b.rep.List
		if list == nil {
			list = b.set.List
		}
		if list == nil {
			list = p.List
		}
		t := Track{Kind: Video, Bandwidth: b.rep.Bandwidth, Width: b.rep.Width, Height: b.rep.Height}
		if k == 1 {
			t.Kind = Audio
		}
		mime := b.rep.MimeType
		if mime == "" {
			mime = b.set.MimeType
		}
		t.Format = "mp4"
		if strings.HasSuffix(mime, "/webm") {
			t.Format = "webm"
		}
		switch {
		case tmpl != nil && tmpl.Media != "":
			t.Fragments, err = fromTemplate(rbase, tmpl, b.rep, total)
		case list != nil:
			t.Fragments, err = fromList(rbase, list)
		default:
			// One file per representation (SegmentBase or only a BaseURL).
			t.Single = true
			t.Fragments = []fetch.Fragment{{URL: rbase, Len: -1}}
		}
		if err != nil {
			return nil, err
		}
		plan.Tracks = append(plan.Tracks, t)
	}
	if len(plan.Tracks) == 0 {
		return nil, errors.New("the DASH manifest has no video or audio")
	}
	return plan, nil
}

func contentType(set *adaptation, rep *representation) string {
	for _, s := range []string{set.ContentType, rep.MimeType, set.MimeType} {
		if t, _, _ := strings.Cut(s, "/"); t == "video" || t == "audio" {
			return t
		}
	}
	return ""
}

// secondaryAudio is commentary or audio description, not the main sound.
func secondaryAudio(set *adaptation) bool {
	for _, r := range set.Roles {
		switch r.V {
		case "commentary", "description", "alternate", "dub":
			return true
		}
	}
	return false
}

// chain resolves BaseURL elements from the outermost in; each level uses the
// first one listed.
func chain(u string, levels ...[]string) (string, error) {
	for _, l := range levels {
		if len(l) == 0 || strings.TrimSpace(l[0]) == "" {
			continue
		}
		var err error
		if u, err = resolve(u, l[0]); err != nil {
			return "", err
		}
	}
	return u, nil
}

var tmplVar = regexp.MustCompile(`\$(RepresentationID|Number|Time|Bandwidth|)(?:%0(\d+)d)?\$`)

// fill substitutes DASH template identifiers.
func fill(t string, rep *representation, number, tm int64) string {
	return tmplVar.ReplaceAllStringFunc(t, func(s string) string {
		g := tmplVar.FindStringSubmatch(s)
		var v int64
		switch g[1] {
		case "":
			return "$"
		case "RepresentationID":
			return rep.ID
		case "Number":
			v = number
		case "Time":
			v = tm
		case "Bandwidth":
			v = int64(rep.Bandwidth)
		}
		if g[2] != "" {
			w, _ := strconv.Atoi(g[2])
			return fmt.Sprintf("%0*d", w, v)
		}
		return strconv.FormatInt(v, 10)
	})
}

func fromTemplate(base string, t *segTemplate, rep *representation, total time.Duration) ([]fetch.Fragment, error) {
	num := atoiOr(t.Start, 1)
	scale := atoiOr(t.Timescale, 1)
	if scale <= 0 {
		return nil, errors.New("the DASH manifest has an invalid timescale")
	}
	var out []fetch.Fragment
	add := func(rel string) error {
		u, err := resolve(base, rel)
		if err != nil {
			return err
		}
		if len(out) > maxSegments {
			return errors.New("the DASH manifest describes too many segments")
		}
		out = append(out, fetch.Fragment{URL: u, Len: -1})
		return nil
	}
	if t.Init != "" {
		if err := add(fill(t.Init, rep, 0, 0)); err != nil {
			return nil, err
		}
	}
	end := int64(math.Ceil(total.Seconds() * float64(scale)))
	if t.Timeline != nil {
		var tm int64
		for i, s := range t.Timeline.S {
			if s.T != "" {
				tm = atoiOr(s.T, tm)
			}
			if s.D <= 0 {
				return nil, errors.New("the DASH manifest has a segment with no duration")
			}
			repeat := s.R
			if repeat < 0 {
				// Repeat until the next S, or the end of the period.
				until := end
				if i+1 < len(t.Timeline.S) && t.Timeline.S[i+1].T != "" {
					until = atoiOr(t.Timeline.S[i+1].T, end)
				}
				repeat = (until-tm+s.D-1)/s.D - 1
			}
			for range repeat + 1 {
				if err := add(fill(t.Media, rep, num, tm)); err != nil {
					return nil, err
				}
				tm += s.D
				num++
			}
		}
		return out, nil
	}
	d := atoiOr(t.Duration, 0)
	if d <= 0 || end <= 0 {
		return nil, errors.New("the DASH manifest doesn't say how long the stream is")
	}
	for i := int64(0); i*d < end; i++ {
		if err := add(fill(t.Media, rep, num+i, i*d)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func fromList(base string, l *segList) ([]fetch.Fragment, error) {
	var out []fetch.Fragment
	add := func(rel, rng string) error {
		u := base
		if rel != "" {
			var err error
			if u, err = resolve(base, rel); err != nil {
				return err
			}
		}
		f := fetch.Fragment{URL: u, Len: -1}
		if rng != "" {
			a, b, ok := strings.Cut(rng, "-")
			s, err1 := strconv.ParseInt(a, 10, 64)
			e, err2 := strconv.ParseInt(b, 10, 64)
			if !ok || err1 != nil || err2 != nil || e < s {
				return errors.New("the DASH manifest has an invalid byte range")
			}
			f.Start, f.Len = s, e-s+1
		}
		out = append(out, f)
		return nil
	}
	if l.Init != nil {
		if err := add(l.Init.URL, l.Init.Range); err != nil {
			return nil, err
		}
	}
	for _, s := range l.URLs {
		if err := add(s.Media, s.Range); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the DASH manifest lists no segments")
	}
	return out, nil
}

func atoiOr(s string, def int64) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
		return v
	}
	return def
}

var isoDur = regexp.MustCompile(`^P(?:0+Y)?(?:0+M)?(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// isoDuration parses the xs:duration forms manifests use, such as
// "PT1H2M3.5S". "" is zero.
func isoDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	g := isoDur.FindStringSubmatch(strings.TrimSpace(s))
	if g == nil {
		return 0, fmt.Errorf("the DASH manifest has an unreadable duration %q", s)
	}
	var secs float64
	for i, unit := range []float64{86400, 3600, 60, 1} {
		if g[i+1] != "" {
			v, _ := strconv.ParseFloat(g[i+1], 64)
			secs += v * unit
		}
	}
	return time.Duration(secs * float64(time.Second)), nil
}
