package parser

import (
	. "luago/compiler/ast"
	. "luago/compiler/lexer"
)

/* recursive descent parser */

func Parse(chunk, chunkName string) *Block {
	lexer := NewLexer(chunk, chunkName)
	block := parseBlock(lexer)
	lexer.NextTokenOfKind(TOKEN_EOF) // 末尾必须是 EOF,否则语法错误
	return block
}
