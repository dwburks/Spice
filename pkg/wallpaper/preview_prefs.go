package wallpaper

import "sync"

// memPreferences is a throwaway in-memory Preferences. The Smart fit preview builds a private
// Config on top of it so the real processor can run with settings that are not saved, without
// ever touching the shared Config or the stored preferences.
type memPreferences struct {
	mu   sync.RWMutex
	bool map[string]bool
	flt  map[string]float64
	num  map[string]int
	str  map[string]string
	list map[string][]string
}

func newMemPreferences() *memPreferences {
	return &memPreferences{
		bool: map[string]bool{}, flt: map[string]float64{}, num: map[string]int{},
		str: map[string]string{}, list: map[string][]string{},
	}
}

func (m *memPreferences) Bool(key string) bool { return m.BoolWithFallback(key, false) }
func (m *memPreferences) BoolWithFallback(key string, fb bool) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.bool[key]; ok {
		return v
	}
	return fb
}
func (m *memPreferences) SetBool(key string, v bool) { m.mu.Lock(); m.bool[key] = v; m.mu.Unlock() }

func (m *memPreferences) Float(key string) float64 { return m.FloatWithFallback(key, 0) }
func (m *memPreferences) FloatWithFallback(key string, fb float64) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.flt[key]; ok {
		return v
	}
	return fb
}
func (m *memPreferences) SetFloat(key string, v float64) { m.mu.Lock(); m.flt[key] = v; m.mu.Unlock() }

func (m *memPreferences) Int(key string) int { return m.IntWithFallback(key, 0) }
func (m *memPreferences) IntWithFallback(key string, fb int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.num[key]; ok {
		return v
	}
	return fb
}
func (m *memPreferences) SetInt(key string, v int) { m.mu.Lock(); m.num[key] = v; m.mu.Unlock() }

func (m *memPreferences) String(key string) string { return m.StringWithFallback(key, "") }
func (m *memPreferences) StringWithFallback(key, fb string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.str[key]; ok {
		return v
	}
	return fb
}
func (m *memPreferences) SetString(key, v string) { m.mu.Lock(); m.str[key] = v; m.mu.Unlock() }

func (m *memPreferences) StringList(key string) []string { return m.StringListWithFallback(key, nil) }
func (m *memPreferences) StringListWithFallback(key string, fb []string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.list[key]; ok {
		return append([]string(nil), v...)
	}
	return fb
}
func (m *memPreferences) SetStringList(key string, v []string) {
	m.mu.Lock()
	m.list[key] = append([]string(nil), v...)
	m.mu.Unlock()
}

func (m *memPreferences) RemoveValue(key string) {
	m.mu.Lock()
	delete(m.bool, key)
	delete(m.flt, key)
	delete(m.num, key)
	delete(m.str, key)
	delete(m.list, key)
	m.mu.Unlock()
}
