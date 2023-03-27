package state

/*
import (
	"luago/number"
	"math"
)

type luaTable struct {
	metatable *luaTable
	_map      map[luaValue]luaValue
	keys      map[luaValue]luaValue // used by next()
	lastKey   luaValue              // used by next()
	changed   bool                  // used by next()
}

func newLuaTable(nArr, nRec int) *luaTable {
	t := &luaTable{}
	if nRec > 0 {
		t._map = make(map[luaValue]luaValue, nRec)
	}
	return t
}

func (self *luaTable) hasMetafield(fieldName string) bool {
	return self.metatable != nil &&
		self.metatable.get(fieldName) != nil
}

func (self *luaTable) len() int {
	return len(self._map)
}

func (self *luaTable) get(key luaValue) luaValue {
	key = _floatToInteger(key)
	return self._map[key]
}

func _floatToInteger(key luaValue) luaValue {
	if f, ok := key.(float64); ok {
		if i, ok := number.FloatToInteger(f); ok {
			return i
		}
	}
	return key
}

func (self *luaTable) put(key, val luaValue) {
	if key == nil {
		panic("table index is nil!")
	}
	if f, ok := key.(float64); ok && math.IsNaN(f) {
		panic("table index is NaN!")
	}

	self.changed = true
	key = _floatToInteger(key)
	if val != nil {
		if self._map == nil {
			self._map = make(map[luaValue]luaValue, 8)
		}
		self._map[key] = val
	} else {
		delete(self._map, key)
	}
}

func (self *luaTable) nextKey(key luaValue) luaValue {
	if self.keys == nil || (key == nil && self.changed) {
		self.initKeys()
		self.changed = false
	}

	nextKey := self.keys[key]
	if nextKey == nil && key != nil && key != self.lastKey {
		panic("invalid key to 'next'")
	}

	return nextKey
}

func (self *luaTable) initKeys() {
	self.keys = make(map[luaValue]luaValue)
	var key luaValue = nil
	for k, v := range self._map {
		if v != nil {
			self.keys[key] = k
			key = k
		}
	}
	self.lastKey = key
}
*/
