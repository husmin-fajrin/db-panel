package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// How auditDetailSummary words a detail field's value.
const (
	sumText   = iota // the string or number as it is
	sumID            // a server or topology id: #3
	sumFlag          // a boolean shown only when true: "ya"
	sumList          // a list: a, b
	sumHours         // hours: 24 jam
	sumTime          // an RFC 3339 time: dd-mm-yyyy HH:MM
	sumJob           // a job id, its first 8 characters
	sumKind          // a job kind (kindLabel)
	sumAction        // a confirmation's action
)

// auditSummaryRunes caps each value of a detail summary.
const auditSummaryRunes = 60

type auditDetailField struct {
	key, label string
	how        int
}

// auditDetailFields are the detail keys the audit handlers write, in the
// order a summary lists them. target_kind and target_id are left out: the
// Target column shows them.
var auditDetailFields = []auditDetailField{
	{"candidate", "kandidat", sumID},
	{"candidate_id", "kandidat", sumID},
	{"mode", "mode", sumText},
	{"accept_partial", "terima sebagian", sumFlag},
	{"accept_unreachable", "terima replika tak terjangkau", sumFlag},
	{"accept_branched", "terima replika bercabang", sumFlag},
	{"action", "aksi", sumAction},
	{"name", "nama", sumText},
	{"unquarantined", "lepas karantina", sumFlag},
	{"repair", "pasang ulang", sumFlag},
	{"mysql_user", "pengguna MySQL", sumText},
	{"host", "host", sumText},
	{"hostname", "nama host", sumText},
	{"source_host", "host sumber", sumText},
	{"source_port", "port sumber", sumText},
	{"master_server_id", "master", sumID},
	{"old_master_server_id", "master lama", sumID},
	{"topology_id", "topologi", sumID},
	{"databases", "database", sumList},
	{"force_dump", "dump ulang", sumFlag},
	{"rebuild_old_master", "bangun ulang master lama", sumFlag},
	{"drop_data", "hapus data", sumFlag},
	{"wipe_list", "kosongkan", sumList},
	{"hours", "lama", sumHours},
	{"until", "sampai", sumTime},
	{"changed", "diubah", sumList},
	{store.SettingTelegramChatID, "chat id", sumText},
	{store.SettingPanelBaseURL, "alamat dasar", sumText},
	{"names", "nama", sumList},
	{"extra_names", "nama tambahan", sumText},
	{auditSalvageFile, "berkas", sumText},
	{"warning", "dengan peringatan", sumFlag},
	{"reason", "alasan", sumText},
	{"job_id", "pekerjaan", sumJob},
	{"kind", "jenis", sumKind},
}

// auditSkippedKeys are detail keys a summary leaves out.
var auditSkippedKeys = map[string]bool{"target_kind": true, "target_id": true}

// confirmActionLabels name the confirmation actions that are not audit
// actions themselves (the detail of confirm_issued and the like).
var confirmActionLabels = map[string]string{
	ActResync:             "Resync ulang",
	ActDetach:             "Lepas replika",
	ActDeleteServer:       "Hapus server",
	ActUnlockMaster:       "Buka kunci master",
	ActPrepareMasterAgain: "Siapkan ulang sebagai master",
	ActRebuildOldMaster:   "Bangun ulang master lama",
	ActCertUpload:         "Pasang sertifikat keamanan panel sendiri",
	ActCertRevert:         "Kembali ke sertifikat panel",
	ActCertNames:          "Ubah nama sertifikat panel",
}

// auditDetailSummary words an audit entry's detail_json for people (design
// spec 5.6): "kandidat: #3; terima sebagian: ya". Known keys come in
// auditDetailFields' order with their own wording, a refused action's
// lengths (<key>_len, <key>_count) as "N karakter" and "N item", other keys
// as "key: value" after them. "" for an empty detail; a detail that is not
// a JSON object is shown as it is, cut. action is not needed by the
// wording today; the keys alone decide it.
func auditDetailSummary(action, detailJSON string) string {
	detailJSON = strings.TrimSpace(detailJSON)
	if detailJSON == "" || detailJSON == "{}" {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(detailJSON)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return cutRunes(detailJSON, auditDetailRunes)
	}
	var parts []string
	seen := map[string]bool{}
	add := func(label, value string) {
		if value != "" {
			parts = append(parts, label+": "+value)
		}
	}
	for _, f := range auditDetailFields {
		if v, ok := m[f.key]; ok {
			seen[f.key] = true
			add(f.label, summaryValue(v, f.how))
		}
		for suffix, unit := range map[string]string{"_len": " karakter", "_count": " item"} {
			if v, ok := m[f.key+suffix]; ok {
				seen[f.key+suffix] = true
				add(f.label, summaryValue(v, sumText)+unit)
			}
		}
	}
	var rest []string
	for k := range m {
		if !seen[k] && !auditSkippedKeys[k] {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	for _, k := range rest {
		add(k, summaryValue(m[k], sumText))
	}
	return strings.Join(parts, "; ")
}

// summaryValue words one value; "" leaves its field out.
func summaryValue(v any, how int) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		switch {
		case how == sumFlag && !x:
			return ""
		case x:
			return "ya"
		}
		return "tidak"
	case []any:
		items := make([]string, 0, len(x))
		for _, it := range x {
			if s := summaryValue(it, sumText); s != "" {
				items = append(items, s)
			}
		}
		return cutRunes(strings.Join(items, ", "), auditSummaryRunes)
	case map[string]any:
		return cutRunes(fmt.Sprint(x), auditSummaryRunes)
	}
	s := fmt.Sprint(v)
	switch how {
	case sumID:
		return "#" + s
	case sumHours:
		return s + " jam"
	case sumTime:
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return fmtDateTime(t)
		}
	case sumJob:
		return cutRunes(s, 8)
	case sumKind:
		return kindLabel(s)
	case sumAction:
		if l, ok := confirmActionLabels[s]; ok {
			return l
		}
		return auditActionLabel(s)
	}
	return cutRunes(s, auditSummaryRunes)
}

// cutRunes is s cut to n runes, with "…" when cut.
func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
