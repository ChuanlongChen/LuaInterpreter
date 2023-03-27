package test

import (
	"fmt"
	"luago/compiler"
)

func TestDumpAndUndump(data []byte, sourceFilePath string) {
	testDump(data, sourceFilePath)
	//	testUnDump()
}

func testDump(data []byte, fileName string) {
	proto := compiler.Compile(string(data), fileName)
	fmt.Printf("%+v\n", proto)
	//	_ = Dump(proto)
}

/*
func testUnDump() {

	data, err := ioutil.ReadFile("D:\\lua_go\\my_luac.out")
	if err != nil {
		panic(err)
	}

	proto := binchunk.Undump(data)
	fmt.Printf("undump:\n%+v\n", proto)

}
*/
