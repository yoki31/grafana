package models

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"slices"
	"unsafe"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/prometheus/common/model"
)

// GroupByAll is a special value defined by alertmanager that can be used in a Route's GroupBy field to aggregate by all possible labels.
const GroupByAll = "..."

// DefaultNotificationSettingsGroupBy are the default required GroupBy fields for notification settings.
var DefaultNotificationSettingsGroupBy = []string{FolderTitleLabel, model.AlertNameLabel}

type ListNotificationSettingsQuery struct {
	OrgID            int64
	ReceiverName     string
	TimeIntervalName string
}

// NotificationSettings represents the settings for sending notifications for a single AlertRule. It is used to
// automatically generate labels and an associated matching route containing the given settings.
type NotificationSettings struct {
	Receiver string `json:"receiver"`

	GroupBy           []string        `json:"group_by,omitempty"`
	GroupWait         *model.Duration `json:"group_wait,omitempty"`
	GroupInterval     *model.Duration `json:"group_interval,omitempty"`
	RepeatInterval    *model.Duration `json:"repeat_interval,omitempty"`
	MuteTimeIntervals []string        `json:"mute_time_intervals,omitempty"`
}

func (s *NotificationSettings) GetUID() string {
	return NameToUid(s.Receiver)
}

// NormalizedGroupBy returns a consistent and ordered GroupBy.
//   - If the GroupBy is empty, it returns nil so that the parent group can be inherited.
//   - If the GroupBy contains the special label '...', it returns only '...'.
//   - Otherwise, it returns the default GroupBy labels followed by any custom labels in sorted order.
//
// To ensure consistent and valid generated routes, this should be used instead of GroupBy when generating fingerprints
// or fingerprint-level routes.
func (s *NotificationSettings) NormalizedGroupBy() []string {
	if len(s.GroupBy) == 0 {
		// Inherit group from parent.
		return nil
	}

	defaultGroupBySet := make(map[string]struct{}, len(DefaultNotificationSettingsGroupBy))
	for _, lbl := range DefaultNotificationSettingsGroupBy {
		defaultGroupBySet[lbl] = struct{}{}
	}

	var customLabels []string
	for _, lbl := range s.GroupBy {
		if lbl == GroupByAll {
			return []string{GroupByAll}
		}
		if _, ok := defaultGroupBySet[lbl]; !ok {
			customLabels = append(customLabels, lbl)
		}
	}

	// Sort the custom labels to ensure consistent ordering while keeping the required labels in the front.
	slices.Sort(customLabels)

	normalized := make([]string, 0, len(DefaultNotificationSettingsGroupBy)+len(customLabels))
	normalized = append(normalized, DefaultNotificationSettingsGroupBy...)
	return append(normalized, customLabels...)
}

// Validate checks if the NotificationSettings object is valid.
// It returns an error if any of the validation checks fail.
// The receiver must be specified.
// GroupWait, GroupInterval, RepeatInterval must be positive durations.
func (s *NotificationSettings) Validate() error {
	if s.Receiver == "" {
		return errors.New("receiver must be specified")
	}
	if s.GroupWait != nil && *s.GroupWait < 0 {
		return errors.New("group wait must be a positive duration")
	}
	if s.GroupInterval != nil && *s.GroupInterval <= 0 {
		return errors.New("group interval must be greater than zero")
	}
	if s.RepeatInterval != nil && *s.RepeatInterval <= 0 {
		return errors.New("repeat interval must be greater than zero")
	}
	return nil
}

// ToLabels converts the NotificationSettings into data.Labels. When added to an AlertRule these labels ensure it will
// match an autogenerated route with the correct settings.
// Labels returned:
//   - AutogeneratedRouteLabel: "true"
//   - AutogeneratedRouteReceiverNameLabel: Receiver
//   - AutogeneratedRouteSettingsHashLabel: Fingerprint (if the NotificationSettings are not all default)
func (s *NotificationSettings) ToLabels() data.Labels {
	result := make(data.Labels, 3)
	result[AutogeneratedRouteLabel] = "true"
	result[AutogeneratedRouteReceiverNameLabel] = s.Receiver
	if !s.IsAllDefault() {
		result[AutogeneratedRouteSettingsHashLabel] = s.Fingerprint().String()
	}
	return result
}

func (s *NotificationSettings) Equals(other *NotificationSettings) bool {
	durationEqual := func(d1, d2 *model.Duration) bool {
		if d1 == nil || d2 == nil {
			return d1 == d2
		}
		return *d1 == *d2
	}
	if s == nil || other == nil {
		return s == nil && other == nil
	}
	if s.Receiver != other.Receiver {
		return false
	}
	if !durationEqual(s.GroupWait, other.GroupWait) {
		return false
	}
	if !durationEqual(s.GroupInterval, other.GroupInterval) {
		return false
	}
	if !durationEqual(s.RepeatInterval, other.RepeatInterval) {
		return false
	}
	if !slices.Equal(s.MuteTimeIntervals, other.MuteTimeIntervals) {
		return false
	}
	sGr := s.GroupBy
	oGr := other.GroupBy
	return slices.Equal(sGr, oGr)
}

// IsAllDefault checks if the NotificationSettings object has all default values for optional fields (all except Receiver) .
func (s *NotificationSettings) IsAllDefault() bool {
	return len(s.GroupBy) == 0 && s.GroupWait == nil && s.GroupInterval == nil && s.RepeatInterval == nil && len(s.MuteTimeIntervals) == 0
}

// NewDefaultNotificationSettings creates a new default NotificationSettings with the specified receiver.
func NewDefaultNotificationSettings(receiver string) NotificationSettings {
	return NotificationSettings{
		Receiver: receiver,
	}
}

// Fingerprint calculates a hash value to uniquely identify a NotificationSettings by its attributes.
// The hash is calculated by concatenating the strings and durations of the NotificationSettings attributes
// and using an invalid UTF-8 sequence as a separator.
func (s *NotificationSettings) Fingerprint() data.Fingerprint {
	h := fnv.New64()
	tmp := make([]byte, 8)

	writeString := func(s string) {
		// save on extra slice allocation when string is converted to bytes.
		_, _ = h.Write(unsafe.Slice(unsafe.StringData(s), len(s))) //nolint:gosec
		// ignore errors returned by Write method because fnv never returns them.
		_, _ = h.Write([]byte{255}) // use an invalid utf-8 sequence as separator
	}
	writeDuration := func(d *model.Duration) {
		if d == nil {
			_, _ = h.Write([]byte{255})
		} else {
			binary.LittleEndian.PutUint64(tmp, uint64(*d))
			_, _ = h.Write(tmp)
			_, _ = h.Write([]byte{255})
		}
	}

	writeString(s.Receiver)
	for _, gb := range s.NormalizedGroupBy() {
		writeString(gb)
	}
	writeDuration(s.GroupWait)
	writeDuration(s.GroupInterval)
	writeDuration(s.RepeatInterval)
	for _, interval := range s.MuteTimeIntervals {
		writeString(interval)
	}
	return data.Fingerprint(h.Sum64())
}
