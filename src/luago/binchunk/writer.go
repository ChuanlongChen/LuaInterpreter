package binchunk

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"os"
)


type dumpState struct {
	out   io.Writer
	order binary.ByteOrder
	err   error
}

func (d *dumpState) write(data interface{}) {
	if d.err == nil {
		d.err = binary.Write(d.out, d.order, data)
	}
}

func (d *dumpState) writeByte(b byte) {
	d.write(b)
}

func (d *dumpState) writeUint32(i uint32) {
	d.write(i)
}

func (d *dumpState) writeString(s string) {
	s_byte := []byte(s)
	size := len(s)
	if size == 0 { // null str
		d.write(uint8(size))
		//	d.writeByte(0)
	} else if size <= 0xFD { //short str
		size++
		d.write(uint8(size))
		d.write(s_byte)
		//d.writeByte(0)
	} else { // long str
		size++
		d.write(uint8(0xFF))
		d.write(uint64(size))
		d.write(s_byte)
		//d.writeByte(0)
	}
}

func (d *dumpState) writeBool(b bool) {
	if b {
		d.writeByte(1)
	} else {
		d.writeByte(0)
	}
}

func (d *dumpState) writeNumber(f float64) {
	d.write(f)
}

func (d *dumpState) writeInteger(f int64) {
	d.write(uint64(f))
}

func (d *dumpState) writeCode(p *Prototype) {
	_len := uint32(len(p.Code))
	d.writeUint32(_len)
	d.write(p.Code)
}

func (d *dumpState) writeConstants(p *Prototype) {
	d.writeUint32(uint32(len(p.Constants)))

	for _, o := range p.Constants {
		switch o := o.(type) {
		case nil:
			d.write(uint8(TAG_NIL))
		case bool:
			{
				d.write(uint8(TAG_BOOLEAN))
				d.writeBool(o)
			}
		case float64:
			{
				d.write(uint8(TAG_NUMBER))
				d.writeNumber(o)
			}
		case int64:
			{
				d.write(uint8(TAG_INTEGER))
				d.writeInteger(o)
			}
		case string:
			{
				if len(o) <= 0xFD {
					d.write(uint8(TAG_SHORT_STR))
				} else {
					d.write(uint8(TAG_LONG_STR))
				}
				d.writeString(o)
			}
		default:
			panic("constant type error")
		}
	}
}

func (d *dumpState) writeUpvalues(p *Prototype) {
	d.writeUint32(uint32(len(p.Upvalues)))

	for _, u := range p.Upvalues {
		d.writeByte(u.Instack)
		d.writeByte(u.Idx)
	}
}

func (d *dumpState) writePrototypes(p *Prototype) {
	d.writeUint32(uint32(len(p.Protos)))

	for _, o := range p.Protos {
		d.dumpFunction(o)
	}
}

func (d *dumpState) writeLineInfo(p *Prototype) {
	d.writeUint32(uint32(len(p.LineInfo)))

	for _, lineInfo := range p.LineInfo {
		d.writeUint32(lineInfo)
	}
}

func (d *dumpState) writeLocVars(p *Prototype) {
	d.writeUint32(uint32(len(p.LocVars)))

	for _, lv := range p.LocVars {
		d.writeString(lv.VarName)
		d.writeUint32(lv.StartPC)
		d.writeUint32(lv.EndPC)
	}
}

func (d *dumpState) writeUpvalueNames(p *Prototype) {
	d.writeUint32(uint32(len(p.UpvalueNames)))
	for _, name := range p.UpvalueNames {
		d.writeString(name)
	}
}

func (d *dumpState) dumpFunction(p *Prototype) {
	d.writeString(p.Source)
	d.writeUint32(p.LineDefined)
	d.writeUint32(p.LastLineDefined)
	d.writeByte(p.NumParams)
	d.writeByte(p.IsVararg)
	d.writeByte(p.MaxStackSize)
	d.writeCode(p)
	d.writeConstants(p)
	d.writeUpvalues(p)
	d.writePrototypes(p)
	d.writeLineInfo(p)
	d.writeLocVars(p)
	d.writeUpvalueNames(p)
}

// 主函数 upvalues数量是1 ：_ENV变量
func (d *dumpState) dumpSizeUpvalues() {
	d.err = binary.Write(d.out, d.order, uint8(0))
}

func (d *dumpState) dumpHeader() {
	_header := header{
		version:         LUAC_VERSION,
		format:          LUAC_FORMAT,
		cintSize:        CINT_SIZE,
		sizetSize:       CSIZET_SIZE,
		instructionSize: INSTRUCTION_SIZE,
		luaIntegerSize:  LUA_INTEGER_SIZE,
		luaNumberSize:   LUA_NUMBER_SIZE,
		luacInt:         LUAC_INT,
		luacNum:         LUAC_NUM,
	}
	copy(_header.signature[:], LUA_SIGNATURE)
	copy(_header.luacData[:], LUAC_DATA)
	d.err = binary.Write(d.out, d.order, _header)
}

func Dump(p *Prototype) error {
	var buffer bytes.Buffer
	d := dumpState{out: &buffer, order: binary.LittleEndian}

	d.dumpHeader()
	d.dumpSizeUpvalues()
	d.dumpFunction(p)
	d.err = writeToFile(buffer)
	return d.err
}

func writeToFile(buffer bytes.Buffer) error {
	fileName := "my_luac.out"
	file, err := os.Create(fileName)
	if err != nil {
		log.Fatal("Error while opening file", err)
		return err
	}
	defer file.Close()

	_, err = file.Write(buffer.Bytes())
	if err != nil {
		log.Fatal("Error while writing file", err)
		return err
	}
	return nil
}
