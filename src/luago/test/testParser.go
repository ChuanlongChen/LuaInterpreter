package test

import (
	"encoding/json"
	"luago/compiler/parser"
)

func TestParser(chunk, chunkName string) {
	ast := parser.Parse(chunk, chunkName)
	b, err := json.MarshalIndent(ast, "", "  ")
	if err != nil {
		panic(err)
	}
	println(string(b))
}
