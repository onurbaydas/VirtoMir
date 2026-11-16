package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/timeline"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// A machine's timeline: what happened to it, from every place that knows (timeline.Fold).

func (s *Server) timelineRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/domains/{uuid}/timeline", s.machineTimeline)
}

// timelineSize is how many events are answered unless ?limit= asks for fewer or more.
const (
	timelineSize = 200
	timelineMax  = 1000
)

// machineTimeline puts together what the activity log, the configuration history, libvirt
// (lifecycle, snapshots), the backup folders and the alert engine know about one machine.
// A source that cannot be read is left out and named in "missing": most of the story is
// better than none, as long as it says what it lacks.
func (s *Server) machineTimeline(w http.ResponseWriter, r *http.Request) {
	uuid := strings.ToLower(r.PathValue("uuid"))
	limit := timelineSize
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, timelineMax)
	}
	var events []timeline.Event
	missing := []string{}

	if s.activity != nil {
		for _, e := range s.activity.List(timelineMax, activity.Filter{Target: uuid}) {
			events = append(events, timeline.Event{Time: e.Time, Kind: timeline.KindAction, User: e.User, Token: e.Token,
				Method: e.Method, Path: e.Path, Status: e.Status, Error: e.Error})
		}
	}
	if s.confighist != nil {
		saved, err := s.confighist.List(uuid)
		if err != nil { // not a machine id at all
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		for _, v := range saved {
			events = append(events, timeline.Event{Time: v.Time, Kind: timeline.KindDefinition, What: v.Source, User: v.User, Version: v.ID})
		}
		live, _ := s.confighist.ListLive(uuid)
		for _, v := range live {
			events = append(events, timeline.Event{Time: v.Time, Kind: timeline.KindRunning, What: v.Source, User: v.User, Version: v.ID})
		}
	}
	if s.timeline != nil {
		for _, o := range s.timeline.List(uuid) {
			events = append(events, timeline.Event{Time: o.Time, Kind: timeline.KindLifecycle, What: o.What, Detail: o.Detail})
		}
	}
	if snaps, err := s.backend.ListSnapshots(uuid); err != nil {
		missing = append(missing, "snapshots")
	} else {
		for _, sn := range snaps {
			events = append(events, timeline.Event{Time: time.Unix(sn.Created, 0).UTC(), Kind: timeline.KindSnapshot, Detail: sn.Name, Ref: sn.Name})
		}
	}
	if b, ok := s.backend.(Backupper); ok && s.schedules != nil {
		if list, err := b.ListBackups(virt.BackupDir(s.schedules.Settings().BackupDir), uuid); err != nil {
			missing = append(missing, "backups")
		} else {
			for _, bk := range list {
				events = append(events, timeline.Event{Time: bk.Created, Kind: timeline.KindBackup, Detail: bk.Note, Ref: bk.ID,
					SizeBytes: bk.SizeBytes, Automatic: bk.Automatic, CheckError: bk.CheckError})
			}
		}
	}
	if s.alerts != nil {
		for _, a := range s.alerts.List() {
			if strings.EqualFold(a.SubjectID, uuid) {
				events = append(events, timeline.Event{Time: a.Started, Kind: timeline.KindAlert, What: string(a.Severity), Detail: a.Message, Ref: a.ID, Until: a.Resolved})
			}
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"events": timeline.Fold(events, limit), "missing": missing})
}
