package main

// mapGetter returns a configGetter closure over a local map. Used by tests
// to feed loadConfigFrom and the *With variants without touching globals.
func mapGetter(m map[string]string) configGetter {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}