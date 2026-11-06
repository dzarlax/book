package handler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/dzarlax/book/internal/calendarapi"
)

type calendarCatalogue struct {
	Groups   []calendarGroup
	Warnings []string
	Known    map[string]calendarapi.Calendar
}

func (h *AdminHandler) loadCatalogue(ctx context.Context, writableOnly bool) calendarCatalogue {
	result := calendarCatalogue{Known: map[string]calendarapi.Calendar{}}
	if !h.cal.Enabled() {
		result.Warnings = []string{"Calendar integration is not configured."}
		return result
	}
	for _, provider := range calendarapi.Providers() {
		calendars, err := h.cal.ListProviderCalendars(ctx, provider)
		if err != nil {
			result.Warnings = append(result.Warnings, providerLabel(provider)+" calendars are unavailable. Existing selections are retained.")
			continue
		}
		group := calendarGroup{Provider: providerLabel(provider)}
		for _, calendar := range calendars {
			result.Known[calendar.ID] = calendar
			if writableOnly && (calendar.ReadOnly || provider != "google") {
				continue
			}
			group.Calendars = append(group.Calendars, calendarOption{ID: calendar.ID, Name: calendar.Name, ReadOnly: calendar.ReadOnly})
		}
		if len(group.Calendars) > 0 {
			result.Groups = append(result.Groups, group)
		}
	}
	return result
}
func selectionVersion(raw, keywords string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s", len(raw), raw, len(keywords), keywords))))
}
func selectionIDs(raw string) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range strings.Split(raw, "\n") {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}
func unavailableSelections(raw string, known map[string]calendarapi.Calendar) []calendarOption {
	result := []calendarOption{}
	for _, id := range selectionIDs(raw) {
		if _, ok := known[id]; !ok {
			provider, _, _ := strings.Cut(id, ":")
			provider, _, _ = strings.Cut(provider, "@")
			result = append(result, calendarOption{ID: id, Name: providerLabel(provider) + " previously selected calendar (unavailable)"})
		}
	}
	return result
}

// mergeSelections only removes hidden selections through a distinct explicit
// removal control. A newly visible calendar was not necessarily shown to the
// submitting form; its old selection must survive a catalogue change.
func mergeSelections(old string, selected, visible, remove []string, mode string, known map[string]calendarapi.Calendar) (string, error) {
	if mode == "all" {
		return "", nil
	}
	if mode != "selected" {
		return "", fmt.Errorf("choose all or selected calendars")
	}
	oldSet, visibleSet, removeSet, chosen := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range selectionIDs(old) {
		oldSet[id] = true
	}
	for _, id := range visible {
		visibleSet[id] = true
	}
	for _, id := range remove {
		if !oldSet[id] {
			return "", fmt.Errorf("unknown removal")
		}
		if _, available := known[id]; available && visibleSet[id] {
			return "", fmt.Errorf("remove available calendars with their selection checkbox")
		}
		removeSet[id] = true
	}
	for _, id := range selected {
		if _, ok := known[id]; !ok && !oldSet[id] {
			return "", fmt.Errorf("selected calendar unavailable")
		}
		chosen[id] = true
	}
	for id := range oldSet {
		_, available := known[id]
		if !removeSet[id] && (!available || !visibleSet[id]) {
			chosen[id] = true
		}
	}
	for id := range removeSet {
		delete(chosen, id)
	}
	if len(chosen) == 0 {
		return "", fmt.Errorf("select at least one calendar or explicitly choose all")
	}
	ids := []string{}
	for id := range chosen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, "\n"), nil
}
