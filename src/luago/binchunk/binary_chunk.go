package binchunk

const (
	LUA_SIGNATURE    = "\x1bLua"
	LUAC_VERSION     = 0x53
	LUAC_FORMAT      = 0
	LUAC_DATA        = "\x19\x93\r\n\x1a\n"
	CINT_SIZE        = 4
	CSIZET_SIZE      = 8
	INSTRUCTION_SIZE = 4
	LUA_INTEGER_SIZE = 8
	LUA_NUMBER_SIZE  = 8
	LUAC_INT         = 0x5678
	LUAC_NUM         = 370.5
)

const (
	TAG_NIL       = 0x00
	TAG_BOOLEAN   = 0x01
	TAG_NUMBER    = 0x03
	TAG_INTEGER   = 0x13
	TAG_SHORT_STR = 0x04
	TAG_LONG_STR  = 0x14
)

type binaryChunk struct {
	header
	sizeUpvalues byte // ?
	mainFunc     *Prototype
}

type header struct {
	signature       [4]byte
	version         byte
	format          byte
	luacData        [6]byte
	cintSize        byte
	sizetSize       byte
	instructionSize byte
	luaIntegerSize  byte
	luaNumberSize   byte
	luacInt         int64
	luacNum         float64
}

// function prototype
type Prototype struct {
	Source          string        // 源文件名
	LineDefined     uint32        // 开始行号
	LastLineDefined uint32        // 结束行号
	NumParams       byte          // 固定参数个数
	IsVararg        byte          // 是否是 Vararg 函数
	MaxStackSize    byte          // 寄存器数量
	Code            []uint32      // 字节码，存放生成的虚拟机指令
	Constants       []interface{} // 常量表
	Upvalues        []Upvalue     // Upvalue列表
	Protos          []*Prototype  // 子函数原型
	LineInfo        []uint32      // 行号表
	LocVars         []LocVar      // 局部变量表
	UpvalueNames    []string      // Upvalue名列表
}

type Upvalue struct {
	Instack byte
	Idx     byte
}

type LocVar struct {
	VarName string
	StartPC uint32
	EndPC   uint32
}

func IsBinaryChunk(data []byte) bool {
	return len(data) > 4 &&
		string(data[:4]) == LUA_SIGNATURE
}

func Undump(data []byte) *Prototype {
	reader := &reader{data}
	reader.checkHeader()
	reader.readByte() // size_upvalues
	return reader.readProto("")
}
