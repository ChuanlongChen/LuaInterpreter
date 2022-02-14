package proc

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"path/filepath"
	"strconv"

	"github.com/go-delve/delve/pkg/goversion"
)

// ErrNotExecutable is returned after attempting to execute a non-executable file
// to begin a debug session.
var ErrNotExecutable = errors.New("not an executable file")

// ErrNotRecorded is returned when an action is requested that is
// only possible on recorded (traced) programs.
var ErrNotRecorded = errors.New("not a recording")

var ErrNoRuntimeAllG = errors.New("could not find goroutine array")

const (
	// UnrecoveredPanic is the name given to the unrecovered panic breakpoint.
	UnrecoveredPanic = "unrecovered-panic"

	// FatalThrow is the name given to the breakpoint triggered when the target process dies because of a fatal runtime error
	FatalThrow = "runtime-fatal-throw"

	unrecoveredPanicID = -1
	fatalThrowID       = -2
)

// ErrProcessExited indicates that the process has exited and contains both
// process id and exit status.
type ErrProcessExited struct {
	Pid    int
	Status int
}

func (pe ErrProcessExited) Error() string {
	return fmt.Sprintf("Process %d has exited with status %d", pe.Pid, pe.Status)
}

// ProcessDetachedError indicates that we detached from the target process.
type ProcessDetachedError struct {
}

func (pe ProcessDetachedError) Error() string {
	return "detached from the process"
}

// PostInitializationSetup handles all of the initialization procedures
// that must happen after Delve creates or attaches to a process.
func PostInitializationSetup(p Process, path string, debugInfoDirs []string, writeBreakpoint WriteBreakpointFn) error {
	entryPoint, err := p.EntryPoint()
	if err != nil {
		return err
	}

	err = p.BinInfo().LoadBinaryInfo(path, entryPoint, debugInfoDirs)
	if err != nil {
		return err
	}
	for _, image := range p.BinInfo().Images {
		if image.loadErr != nil {
			return image.loadErr
		}
	}

	g, _ := GetG(p.CurrentThread())
	p.SetSelectedGoroutine(g)

	createUnrecoveredPanicBreakpoint(p, writeBreakpoint)
	createFatalThrowBreakpoint(p, writeBreakpoint)

	return nil
}

// FindFileLocation returns the PC for a given file:line.
// Assumes that `file` is normalized to lower case and '/' on Windows.
func FindFileLocation(p Process, fileName string, lineno int) ([]uint64, error) {
	pcs, err := p.BinInfo().LineToPC(fileName, lineno)
	if err != nil {
		return nil, err
	}
	var fn *Function
	for i := range pcs {
		if fn == nil || pcs[i] < fn.Entry || pcs[i] >= fn.End {
			fn = p.BinInfo().PCToFunc(pcs[i])
		}
		if fn != nil && fn.Entry == pcs[i] {
			pcs[i], _ = FirstPCAfterPrologue(p, fn, true)
		}
	}
	return pcs, nil
}

// ErrFunctionNotFound is returned when failing to find the
// function named 'FuncName' within the binary.
type ErrFunctionNotFound struct {
	FuncName string
}

func (err *ErrFunctionNotFound) Error() string {
	return fmt.Sprintf("Could not find function %s\n", err.FuncName)
}

// FindFunctionLocation finds address of a function's line
// If lineOffset is passed FindFunctionLocation will return the address of that line
func FindFunctionLocation(p Process, funcName string, lineOffset int) ([]uint64, error) {
	bi := p.BinInfo()
	origfn := bi.LookupFunc[funcName]
	if origfn == nil {
		return nil, &ErrFunctionNotFound{funcName}
	}

	if lineOffset <= 0 {
		r := make([]uint64, 0, len(origfn.InlinedCalls)+1)
		if origfn.Entry > 0 {
			// add concrete implementation of the function
			pc, err := FirstPCAfterPrologue(p, origfn, false)
			if err != nil {
				return nil, err
			}
			r = append(r, pc)
		}
		// add inlined calls to the function
		for _, call := range origfn.InlinedCalls {
			r = append(r, call.LowPC)
		}
		if len(r) == 0 {
			return nil, &ErrFunctionNotFound{funcName}
		}
		return r, nil
	}
	filename, lineno := origfn.cu.lineInfo.PCToLine(origfn.Entry, origfn.Entry)
	return bi.LineToPC(filename, lineno+lineOffset)
}

// Next continues execution until the next source line.
func Next(dbp *Target) (err error) {
	if _, err := dbp.Valid(); err != nil {
		return err
	}
	if dbp.Breakpoints().HasInternalBreakpoints() {
		return fmt.Errorf("next while nexting")
	}

	if err = next(dbp, false, false); err != nil {
		dbp.ClearInternalBreakpoints()
		return
	}

	return Continue(dbp)
}

// Continue continues execution of the debugged
// process. It will continue until it hits a breakpoint
// or is otherwise stopped.
func Continue(dbp *Target) error {
	if _, err := dbp.Valid(); err != nil {
		return err
	}
	for _, thread := range dbp.ThreadList() {
		thread.Common().returnValues = nil
	}
	dbp.CheckAndClearManualStopRequest()
	defer func() {
		// Make sure we clear internal breakpoints if we simultaneously receive a
		// manual stop request and hit a breakpoint.
		if dbp.CheckAndClearManualStopRequest() {
			dbp.ClearInternalBreakpoints()
		}
	}()
	for {
		if dbp.CheckAndClearManualStopRequest() {
			dbp.ClearInternalBreakpoints()
			return nil
		}
		dbp.ClearAllGCache()
		trapthread, err := dbp.ContinueOnce()
		if err != nil {
			return err
		}

		threads := dbp.ThreadList()

		callInjectionDone, err := callInjectionProtocol(dbp, threads)
		if err != nil {
			return err
		}

		if err := pickCurrentThread(dbp, trapthread, threads); err != nil {
			return err
		}

		curthread := dbp.CurrentThread()
		curbp := curthread.Breakpoint()

		switch {
		case curbp.Breakpoint == nil:
			// runtime.Breakpoint, manual stop or debugCallV1-related stop
			recorded, _ := dbp.Recorded()
			if recorded {
				return conditionErrors(threads)
			}

			loc, err := curthread.Location()
			if err != nil || loc.Fn == nil {
				return conditionErrors(threads)
			}
			g, _ := GetG(curthread)
			arch := dbp.BinInfo().Arch

			switch {
			case loc.Fn.Name == "runtime.breakpoint":
				// In linux-arm64, PtraceSingleStep seems cannot step over BRK instruction
				// (linux-arm64 feature or kernel bug maybe).
				if !arch.BreakInstrMovesPC() {
					curthread.SetPC(loc.PC + uint64(arch.BreakpointSize()))
				}
				// Single-step current thread until we exit runtime.breakpoint and
				// runtime.Breakpoint.
				// On go < 1.8 it was sufficient to single-step twice on go1.8 a change
				// to the compiler requires 4 steps.
				if err := stepInstructionOut(dbp, curthread, "runtime.breakpoint", "runtime.Breakpoint"); err != nil {
					return err
				}
				return conditionErrors(threads)
			case g == nil || dbp.fncallForG[g.ID] == nil:
				// a hardcoded breakpoint somewhere else in the code (probably cgo), or manual stop in cgo
				if !arch.BreakInstrMovesPC() {
					bpsize := arch.BreakpointSize()
					bp := make([]byte, bpsize)
					_, err = dbp.CurrentThread().ReadMemory(bp, uintptr(loc.PC))
					if bytes.Equal(bp, arch.BreakpointInstruction()) {
						curthread.SetPC(loc.PC + uint64(bpsize))
					}
				}
				return conditionErrors(threads)
			}
		case curbp.Active && curbp.Internal:
			switch curbp.Kind {
			case StepBreakpoint:
				// See description of proc.(*Process).next for the meaning of StepBreakpoints
				if err := conditionErrors(threads); err != nil {
					return err
				}
				regs, err := curthread.Registers(false)
				if err != nil {
					return err
				}
				pc := regs.PC()
				text, err := disassemble(curthread, regs, dbp.Breakpoints(), dbp.BinInfo(), pc, pc+uint64(dbp.BinInfo().Arch.MaxInstructionLength()), true)
				if err != nil {
					return err
				}
				// here we either set a breakpoint into the destination of the CALL
				// instruction or we determined that the called function is hidden,
				// either way we need to resume execution
				if err = setStepIntoBreakpoint(dbp, text, SameGoroutineCondition(dbp.SelectedGoroutine())); err != nil {
					return err
				}
			default:
				curthread.Common().returnValues = curbp.Breakpoint.returnInfo.Collect(curthread)
				if err := dbp.ClearInternalBreakpoints(); err != nil {
					return err
				}
				return conditionErrors(threads)
			}
		case curbp.Active:
			onNextGoroutine, err := onNextGoroutine(curthread, dbp.Breakpoints())
			if err != nil {
				return err
			}
			if onNextGoroutine {
				err := dbp.ClearInternalBreakpoints()
				if err != nil {
					return err
				}
			}
			if curbp.Name == UnrecoveredPanic {
				dbp.ClearInternalBreakpoints()
			}
			return conditionErrors(threads)
		default:
			// not a manual stop, not on runtime.Breakpoint, not on a breakpoint, just repeat
		}
		if callInjectionDone {
			// a call injection was finished, don't let a breakpoint with a failed
			// condition or a step breakpoint shadow this.
			return conditionErrors(threads)
		}
	}
}

func conditionErrors(threads []Thread) error {
	var condErr error
	for _, th := range threads {
		if bp := th.Breakpoint(); bp.Breakpoint != nil && bp.CondError != nil {
			if condErr == nil {
				condErr = bp.CondError
			} else {
				return fmt.Errorf("multiple errors evaluating conditions")
			}
		}
	}
	return condErr
}

// pick a new dbp.currentThread, with the following priority:
// 	- a thread with onTriggeredInternalBreakpoint() == true
// 	- a thread with onTriggeredBreakpoint() == true (prioritizing trapthread)
// 	- trapthread
func pickCurrentThread(dbp Process, trapthread Thread, threads []Thread) error {
	for _, th := range threads {
		if bp := th.Breakpoint(); bp.Active && bp.Internal {
			return dbp.SwitchThread(th.ThreadID())
		}
	}
	if bp := trapthread.Breakpoint(); bp.Active {
		return dbp.SwitchThread(trapthread.ThreadID())
	}
	for _, th := range threads {
		if bp := th.Breakpoint(); bp.Active {
			return dbp.SwitchThread(th.ThreadID())
		}
	}
	return dbp.SwitchThread(trapthread.ThreadID())
}

// stepInstructionOut repeatedly calls StepInstruction until the current
// function is neither fnname1 or fnname2.
// This function is used to step out of runtime.Breakpoint as well as
// runtime.debugCallV1.
func stepInstructionOut(dbp Process, curthread Thread, fnname1, fnname2 string) error {
	for {
		if err := curthread.StepInstruction(); err != nil {
			return err
		}
		loc, err := curthread.Location()
		if err != nil || loc.Fn == nil || (loc.Fn.Name != fnname1 && loc.Fn.Name != fnname2) {
			g, _ := GetG(curthread)
			selg := dbp.SelectedGoroutine()
			if g != nil && selg != nil && g.ID == selg.ID {
				selg.CurrentLoc = *loc
			}
			return curthread.SetCurrentBreakpoint(true)
		}
	}
}

// Step will continue until another source line is reached.
// Will step into functions.
func Step(dbp *Target) (err error) {
	if _, err := dbp.Valid(); err != nil {
		return err
	}
	if dbp.Breakpoints().HasInternalBreakpoints() {
		return fmt.Errorf("next while nexting")
	}

	if err = next(dbp, true, false); err != nil {
		switch err.(type) {
		case ErrThreadBlocked: // Noop
		default:
			dbp.ClearInternalBreakpoints()
			return
		}
	}

	return Continue(dbp)
}

// SameGoroutineCondition returns an expression that evaluates to true when
// the current goroutine is g.
func SameGoroutineCondition(g *G) ast.Expr {
	if g == nil {
		return nil
	}
	return &ast.BinaryExpr{
		Op: token.EQL,
		X: &ast.SelectorExpr{
			X: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "runtime"},
				Sel: &ast.Ident{Name: "curg"},
			},
			Sel: &ast.Ident{Name: "goid"},
		},
		Y: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(g.ID)},
	}
}

func frameoffCondition(frameoff int64) ast.Expr {
	return &ast.BinaryExpr{
		Op: token.EQL,
		X: &ast.SelectorExpr{
			X:   &ast.Ident{Name: "runtime"},
			Sel: &ast.Ident{Name: "frameoff"},
		},
		Y: &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(frameoff, 10)},
	}
}

func andFrameoffCondition(cond ast.Expr, frameoff int64) ast.Expr {
	if cond == nil {
		return nil
	}
	return &ast.BinaryExpr{
		Op: token.LAND,
		X:  cond,
		Y:  frameoffCondition(frameoff),
	}
}

// StepOut will continue until the current goroutine exits the
// function currently being executed or a deferred function is executed
func StepOut(dbp *Target) error {
	if _, err := dbp.Valid(); err != nil {
		return err
	}
	if dbp.Breakpoints().HasInternalBreakpoints() {
		return fmt.Errorf("next while nexting")
	}

	selg := dbp.SelectedGoroutine()
	curthread := dbp.CurrentThread()

	topframe, retframe, err := topframe(selg, curthread)
	if err != nil {
		return err
	}

	success := false
	defer func() {
		if !success {
			dbp.ClearInternalBreakpoints()
		}
	}()

	if topframe.Inlined {
		if err := next(dbp, false, true); err != nil {
			return err
		}

		success = true
		return Continue(dbp)
	}

	sameGCond := SameGoroutineCondition(selg)
	retFrameCond := andFrameoffCondition(sameGCond, retframe.FrameOffset())

	var deferpc uint64
	if filepath.Ext(topframe.Current.File) == ".go" {
		if topframe.TopmostDefer != nil && topframe.TopmostDefer.DeferredPC != 0 {
			deferfn := dbp.BinInfo().PCToFunc(topframe.TopmostDefer.DeferredPC)
			deferpc, err = FirstPCAfterPrologue(dbp, deferfn, false)
			if err != nil {
				return err
			}
		}
	}

	if deferpc != 0 && deferpc != topframe.Current.PC {
		bp, err := dbp.SetBreakpoint(deferpc, NextDeferBreakpoint, sameGCond)
		if err != nil {
			if _, ok := err.(BreakpointExistsError); !ok {
				return err
			}
		}
		if bp != nil {
			// For StepOut we do not want to step into the deferred function
			// when it's called by runtime.deferreturn so we do not populate
			// DeferReturns.
			bp.DeferReturns = []uint64{}
		}
	}

	if topframe.Ret == 0 && deferpc == 0 {
		return errors.New("nothing to stepout to")
	}

	if topframe.Ret != 0 {
		bp, err := dbp.SetBreakpoint(topframe.Ret, NextBreakpoint, retFrameCond)
		if err != nil {
			if _, isexists := err.(BreakpointExistsError); !isexists {
				return err
			}
		}
		if bp != nil {
			configureReturnBreakpoint(dbp.BinInfo(), bp, &topframe, retFrameCond)
		}
	}

	if bp := curthread.Breakpoint(); bp.Breakpoint == nil {
		curthread.SetCurrentBreakpoint(false)
	}

	success = true
	return Continue(dbp)
}

// StepInstruction will continue the current thread for exactly
// one instruction. This method affects only the thread
// associated with the selected goroutine. All other
// threads will remain stopped.
func StepInstruction(dbp *Target) (err error) {
	thread := dbp.CurrentThread()
	g := dbp.SelectedGoroutine()
	if g != nil {
		if g.Thread == nil {
			// Step called on parked goroutine
			if _, err := dbp.SetBreakpoint(g.PC, NextBreakpoint,
				SameGoroutineCondition(dbp.SelectedGoroutine())); err != nil {
				return err
			}
			return Continue(dbp)
		}
		thread = g.Thread
	}
	dbp.ClearAllGCache()
	if ok, err := dbp.Valid(); !ok {
		return err
	}
	thread.Breakpoint().Clear()
	err = thread.StepInstruction()
	if err != nil {
		return err
	}
	err = thread.SetCurrentBreakpoint(true)
	if err != nil {
		return err
	}
	if tg, _ := GetG(thread); tg != nil {
		dbp.SetSelectedGoroutine(tg)
	}
	return nil
}

// GoroutinesInfo searches for goroutines starting at index 'start', and
// returns an array of up to 'count' (or all found elements, if 'count' is 0)
// G structures representing the information Delve care about from the internal
// runtime G structure.
// GoroutinesInfo also returns the next index to be used as 'start' argument
// while scanning for all available goroutines, or -1 if there was an error
// or if the index already reached the last possible value.
func GoroutinesInfo(dbp *Target, start, count int) ([]*G, int, error) {
	if _, err := dbp.Valid(); err != nil {
		return nil, -1, err
	}
	if dbp.gcache.allGCache != nil {
		// We can't use the cached array to fulfill a subrange request
		if start == 0 && (count == 0 || count >= len(dbp.gcache.allGCache)) {
			return dbp.gcache.allGCache, -1, nil
		}
	}

	var (
		threadg = map[int]*G{}
		allg    []*G
	)

	threads := dbp.ThreadList()
	for _, th := range threads {
		if th.Blocked() {
			continue
		}
		g, _ := GetG(th)
		if g != nil {
			threadg[g.ID] = g
		}
	}

	allgptr, allglen, err := dbp.gcache.getRuntimeAllg(dbp.BinInfo(), dbp.CurrentThread())
	if err != nil {
		return nil, -1, err
	}

	for i := uint64(start); i < allglen; i++ {
		if count != 0 && len(allg) >= count {
			return allg, int(i), nil
		}
		gvar, err := newGVariable(dbp.CurrentThread(), uintptr(allgptr+(i*uint64(dbp.BinInfo().Arch.PtrSize()))), true)
		if err != nil {
			allg = append(allg, &G{Unreadable: err})
			continue
		}
		g, err := gvar.parseG()
		if err != nil {
			allg = append(allg, &G{Unreadable: err})
			continue
		}
		if thg, allocated := threadg[g.ID]; allocated {
			loc, err := thg.Thread.Location()
			if err != nil {
				return nil, -1, err
			}
			g.Thread = thg.Thread
			// Prefer actual thread location information.
			g.CurrentLoc = *loc
			g.SystemStack = thg.SystemStack
		}
		if g.Status != Gdead {
			allg = append(allg, g)
		}
		dbp.gcache.addGoroutine(g)
	}
	if start == 0 {
		dbp.gcache.allGCache = allg
	}

	return allg, -1, nil
}

// FindGoroutine returns a G struct representing the goroutine
// specified by `gid`.
func FindGoroutine(dbp *Target, gid int) (*G, error) {
	if selg := dbp.SelectedGoroutine(); (gid == -1) || (selg != nil && selg.ID == gid) || (selg == nil && gid == 0) {
		// Return the currently selected goroutine in the following circumstances:
		//
		// 1. if the caller asks for gid == -1 (because that's what a goroutine ID of -1 means in our API).
		// 2. if gid == selg.ID.
		//    this serves two purposes: (a) it's an optimizations that allows us
		//    to avoid reading any other goroutine and, more importantly, (b) we
		//    could be reading an incorrect value for the goroutine ID of a thread.
		//    This condition usually happens when a goroutine calls runtime.clone
		//    and for a short period of time two threads will appear to be running
		//    the same goroutine.
		// 3. if the caller asks for gid == 0 and the selected goroutine is
		//    either 0 or nil.
		//    Goroutine 0 is special, it either means we have no current goroutine
		//    (for example, running C code), or that we are running on a speical
		//    stack (system stack, signal handling stack) and we didn't properly
		//    detect it.
		//    Since there could be multiple goroutines '0' running simultaneously
		//    if the user requests it return the one that's already selected or
		//    nil if there isn't a selected goroutine.
		return selg, nil
	}

	if gid == 0 {
		return nil, fmt.Errorf("Unknown goroutine %d", gid)
	}

	// Calling GoroutinesInfo could be slow if there are many goroutines
	// running, check if a running goroutine has been requested first.
	for _, thread := range dbp.ThreadList() {
		g, _ := GetG(thread)
		if g != nil && g.ID == gid {
			return g, nil
		}
	}

	if g := dbp.gcache.partialGCache[gid]; g != nil {
		return g, nil
	}

	const goroutinesInfoLimit = 10
	nextg := 0
	for nextg >= 0 {
		var gs []*G
		var err error
		gs, nextg, err = GoroutinesInfo(dbp, nextg, goroutinesInfoLimit)
		if err != nil {
			return nil, err
		}
		for i := range gs {
			if gs[i].ID == gid {
				if gs[i].Unreadable != nil {
					return nil, gs[i].Unreadable
				}
				return gs[i], nil
			}
		}
	}

	return nil, fmt.Errorf("Unknown goroutine %d", gid)
}

// ConvertEvalScope returns a new EvalScope in the context of the
// specified goroutine ID and stack frame.
// If deferCall is > 0 the eval scope will be relative to the specified deferred call.
func ConvertEvalScope(dbp *Target, gid, frame, deferCall int) (*EvalScope, error) {
	if _, err := dbp.Valid(); err != nil {
		return nil, err
	}
	ct := dbp.CurrentThread()
	g, err := FindGoroutine(dbp, gid)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return ThreadScope(ct)
	}

	var thread MemoryReadWriter
	if g.Thread == nil {
		thread = ct
	} else {
		thread = g.Thread
	}

	var opts StacktraceOptions
	if deferCall > 0 {
		opts = StacktraceReadDefers
	}

	locs, err := g.Stacktrace(frame+1, opts)
	if err != nil {
		return nil, err
	}

	if frame >= len(locs) {
		return nil, fmt.Errorf("Frame %d does not exist in goroutine %d", frame, gid)
	}

	if deferCall > 0 {
		if deferCall-1 >= len(locs[frame].Defers) {
			return nil, fmt.Errorf("Frame %d only has %d deferred calls", frame, len(locs[frame].Defers))
		}

		d := locs[frame].Defers[deferCall-1]
		if d.Unreadable != nil {
			return nil, d.Unreadable
		}

		return d.EvalScope(ct)
	}

	return FrameToScope(dbp.BinInfo(), thread, g, locs[frame:]...), nil
}

// FrameToScope returns a new EvalScope for frames[0].
// If frames has at least two elements all memory between
// frames[0].Regs.SP() and frames[1].Regs.CFA will be cached.
// Otherwise all memory between frames[0].Regs.SP() and frames[0].Regs.CFA
// will be cached.
func FrameToScope(bi *BinaryInfo, thread MemoryReadWriter, g *G, frames ...Stackframe) *EvalScope {
	// Creates a cacheMem that will preload the entire stack frame the first
	// time any local variable is read.
	// Remember that the stack grows downward in memory.
	minaddr := frames[0].Regs.SP()
	var maxaddr uint64
	if len(frames) > 1 && frames[0].SystemStack == frames[1].SystemStack {
		maxaddr = uint64(frames[1].Regs.CFA)
	} else {
		maxaddr = uint64(frames[0].Regs.CFA)
	}
	if maxaddr > minaddr && maxaddr-minaddr < maxFramePrefetchSize {
		thread = cacheMemory(thread, uintptr(minaddr), int(maxaddr-minaddr))
	}

	s := &EvalScope{Location: frames[0].Call, Regs: frames[0].Regs, Mem: thread, g: g, BinInfo: bi, frameOffset: frames[0].FrameOffset()}
	s.PC = frames[0].lastpc
	return s
}

// createUnrecoveredPanicBreakpoint creates the unrecoverable-panic breakpoint.
// This function is meant to be called by implementations of the Process interface.
func createUnrecoveredPanicBreakpoint(p Process, writeBreakpoint WriteBreakpointFn) {
	panicpcs, err := FindFunctionLocation(p, "runtime.startpanic", 0)
	if _, isFnNotFound := err.(*ErrFunctionNotFound); isFnNotFound {
		panicpcs, err = FindFunctionLocation(p, "runtime.fatalpanic", 0)
	}
	if err == nil {
		bp, err := p.Breakpoints().SetWithID(unrecoveredPanicID, panicpcs[0], writeBreakpoint)
		if err == nil {
			bp.Name = UnrecoveredPanic
			bp.Variables = []string{"runtime.curg._panic.arg"}
		}
	}
}

func createFatalThrowBreakpoint(p Process, writeBreakpoint WriteBreakpointFn) {
	fatalpcs, err := FindFunctionLocation(p, "runtime.fatalthrow", 0)
	if err == nil {
		bp, err := p.Breakpoints().SetWithID(fatalThrowID, fatalpcs[0], writeBreakpoint)
		if err == nil {
			bp.Name = FatalThrow
		}
	}
}

// FirstPCAfterPrologue returns the address of the first
// instruction after the prologue for function fn.
// If sameline is set FirstPCAfterPrologue will always return an
// address associated with the same line as fn.Entry.
func FirstPCAfterPrologue(p Process, fn *Function, sameline bool) (uint64, error) {
	pc, _, line, ok := fn.cu.lineInfo.PrologueEndPC(fn.Entry, fn.End)
	if ok {
		if !sameline {
			return pc, nil
		}
		_, entryLine := fn.cu.lineInfo.PCToLine(fn.Entry, fn.Entry)
		if entryLine == line {
			return pc, nil
		}
	}

	pc, err := firstPCAfterPrologueDisassembly(p, fn, sameline)
	if err != nil {
		return fn.Entry, err
	}

	if pc == fn.Entry {
		// Look for the first instruction with the stmt flag set, so that setting a
		// breakpoint with file:line and with the function name always result on
		// the same instruction being selected.
		if pc2, _, _, ok := fn.cu.lineInfo.FirstStmtForLine(fn.Entry, fn.End); ok {
			return pc2, nil
		}
	}

	return pc, nil
}

func setAsyncPreemptOff(p *Target, v int64) {
	logger := p.BinInfo().logger
	if producer := p.BinInfo().Producer(); producer == "" || !goversion.ProducerAfterOrEqual(producer, 1, 14) {
		return
	}
	scope := globalScope(p.BinInfo(), p.BinInfo().Images[0], p.CurrentThread())
	debugv, err := scope.findGlobal("runtime", "debug")
	if err != nil || debugv.Unreadable != nil {
		logger.Warnf("could not find runtime/debug variable (or unreadable): %v %v", err, debugv.Unreadable)
		return
	}
	asyncpreemptoffv, err := debugv.structMember("asyncpreemptoff")
	if err != nil {
		logger.Warnf("could not find asyncpreemptoff field: %v", err)
		return
	}
	asyncpreemptoffv.loadValue(loadFullValue)
	if asyncpreemptoffv.Unreadable != nil {
		logger.Warnf("asyncpreemptoff field unreadable: %v", asyncpreemptoffv.Unreadable)
		return
	}
	p.asyncPreemptChanged = true
	p.asyncPreemptOff, _ = constant.Int64Val(asyncpreemptoffv.Value)

	err = scope.setValue(asyncpreemptoffv, newConstant(constant.MakeInt64(v), scope.Mem), "")
	logger.Warnf("could not set asyncpreemptoff %v", err)
}
