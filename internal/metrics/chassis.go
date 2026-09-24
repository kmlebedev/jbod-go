// SPDX-License-Identifier: BSD-2-Clause

package metrics

import (
	"cmp"
	"slices"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// A shelf with two I/O modules is two SCSI enclosures with one enclosure
// identifier, and each module reports every element of the shelf: the same
// sensor, the same supply, the same bay, twice. The element series —
// jbod_component_info, the flags, the sensors and their thresholds, the
// slot mapping — are published once per shelf instead, addressed by
// enclosure_id and component_id, with no enclosure label. On a WD H4060-J
// that halved them: 870 series of 3327, and of the pairs that differed at
// all, the thresholds never did and the readings by a degree or a third of
// an ampere, which is two reads a moment apart.
//
// The answer used for an element is the one from the module best placed to
// give it (see rank): a module that has access to the element over one that
// answers "No access allowed" for it — which is how each module of that
// shelf reports the thirty bays the other owns — and one whose collection
// was complete over one whose was not. With no enclosure label the series
// does not change when the answer comes from the other module, so losing a
// module is not a gap in every element series of the shelf. Whether each
// module answered is still per module: jbod_collection_complete,
// jbod_ses_page_read, jbod_enclosure_health and jbod_enclosure_components
// keep the enclosure label.

// shelf is the elements of one enclosure identifier as they are published.
type shelf struct {
	id         string
	components []jbod.Component
}

// shelves merges the modules that share an enclosure identifier. A shelf
// with one module, or an identifier that is only an address, is a shelf of
// its own, unchanged.
func shelves(snapshot jbod.Snapshot) []shelf {
	statuses := slices.Clone(snapshot.Status)
	// The modules are ranked in a fixed order, so which one wins a tie does
	// not depend on the order the collection finished in.
	slices.SortStableFunc(statuses, func(a, b jbod.EnclosureStatus) int {
		return cmp.Compare(a.Enclosure, b.Enclosure)
	})
	type pick struct {
		component jbod.Component
		rank      int
	}
	var order []string
	chosen := map[string]map[string]pick{}
	for _, status := range statuses {
		byIndex, seen := chosen[status.Address]
		if !seen {
			byIndex = map[string]pick{}
			chosen[status.Address] = byIndex
			order = append(order, status.Address)
		}
		for _, c := range status.Components {
			r := rank(c, status.Collection.Complete)
			if current, ok := byIndex[c.Index]; ok && current.rank <= r {
				continue
			}
			byIndex[c.Index] = pick{c, r}
		}
	}
	result := make([]shelf, 0, len(order))
	for _, id := range order {
		components := make([]jbod.Component, 0, len(chosen[id]))
		for _, p := range chosen[id] {
			components = append(components, p.component)
		}
		slices.SortFunc(components, func(a, b jbod.Component) int {
			if a.TypeIndex != b.TypeIndex {
				return cmp.Compare(a.TypeIndex, b.TypeIndex)
			}
			return cmp.Compare(a.Element, b.Element)
		})
		result = append(result, shelf{id: id, components: components})
	}
	return result
}

// rank orders the answers of the modules for one element, lowest first.
func rank(c jbod.Component, complete bool) int {
	switch {
	case c.Declared || c.Err.Present():
		// Declared by the configuration and reported by no status page,
		// or not readable: an element nobody described.
		return 3
	case c.NoAccess():
		return 2
	case !complete:
		return 1
	default:
		return 0
	}
}
