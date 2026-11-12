// Package attrs handles issue attributes for the service and the stores.
package attrs

// Clone copies attributes, returning nil when there are none, so callers never share a map
// with stored state.
func Clone(attributes map[string]string) map[string]string {
	if len(attributes) == 0 {
		return nil
	}
	out := make(map[string]string, len(attributes))
	for k, v := range attributes {
		out[k] = v
	}
	return out
}
