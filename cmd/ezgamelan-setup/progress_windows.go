//go:build windows

package main

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/r3t2/ezgamelan/internal/setup"
	"golang.org/x/sys/windows"
)

const (
	installWindowClass = "EZGameLANSetupProgressWindow"
	installWindowTitle = "正在安装 EZGameLAN"

	installWMCreate   = 0x0001
	installWMDestroy  = 0x0002
	installWMClose    = 0x0010
	installWMSetFont  = 0x0030
	installWMProgress = 0x8001
	installWMComplete = 0x8002

	installWSChild   = 0x40000000
	installWSVisible = 0x10000000
	installWSCaption = 0x00C00000
	installWSSysMenu = 0x00080000

	installColorWindow   = 5
	installIDCArrow      = 32512
	installDefaultFont   = 17
	installPBMSetPos     = 0x0402
	installPBMSetRange32 = 0x0406
	installPBSmooth      = 0x00000001
	installICCProgress   = 0x00000020
)

var (
	installUser32 = windows.NewLazySystemDLL("user32.dll")
	installGDI32  = windows.NewLazySystemDLL("gdi32.dll")
	installComctl = windows.NewLazySystemDLL("comctl32.dll")

	installRegisterClass = installUser32.NewProc("RegisterClassExW")
	installCreateWindow  = installUser32.NewProc("CreateWindowExW")
	installDefWindowProc = installUser32.NewProc("DefWindowProcW")
	installShowWindow    = installUser32.NewProc("ShowWindow")
	installUpdateWindow  = installUser32.NewProc("UpdateWindow")
	installGetMessage    = installUser32.NewProc("GetMessageW")
	installTranslate     = installUser32.NewProc("TranslateMessage")
	installDispatch      = installUser32.NewProc("DispatchMessageW")
	installPostQuit      = installUser32.NewProc("PostQuitMessage")
	installPostMessage   = installUser32.NewProc("PostMessageW")
	installLoadCursor    = installUser32.NewProc("LoadCursorW")
	installDestroyWindow = installUser32.NewProc("DestroyWindow")
	installSetText       = installUser32.NewProc("SetWindowTextW")
	installSendMessage   = installUser32.NewProc("SendMessageW")
	installGetStock      = installGDI32.NewProc("GetStockObject")
	installInitControls  = installComctl.NewProc("InitCommonControlsEx")

	installClassOnce sync.Once
	installClassErr  error
	installUI        installProgressState
)

type installProgressState struct {
	mu      sync.Mutex
	window  uintptr
	status  uintptr
	percent uintptr
	bar     uintptr
	current setup.Progress
	err     error
}

type installPoint struct {
	X int32
	Y int32
}

type installMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   installPoint
	Private uint32
}

type installWindowClassEx struct {
	Size       uint32
	Style      uint32
	WindowProc uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

type initCommonControlsEx struct {
	Size    uint32
	Classes uint32
}

func runInstallProgress() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	installClassOnce.Do(registerInstallClass)
	if installClassErr != nil {
		return installClassErr
	}
	installUI = installProgressState{}
	window, _, callErr := installCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(installUTF16(installWindowClass))),
		uintptr(unsafe.Pointer(installUTF16(installWindowTitle))),
		installWSCaption|installWSSysMenu,
		0x80000000,
		0x80000000,
		560,
		220,
		0,
		0,
		0,
		0,
	)
	if window == 0 {
		return fmt.Errorf("创建安装进度窗口: %w", callErr)
	}
	installUI.window = window
	installShowWindow.Call(window, 5)
	installUpdateWindow.Call(window)

	go func() {
		err := install(reportInstallProgress)
		installUI.mu.Lock()
		installUI.err = err
		installUI.mu.Unlock()
		installPostMessage.Call(window, installWMComplete, 0, 0)
	}()

	var message installMessage
	for {
		result, _, err := installGetMessage.Call(
			uintptr(unsafe.Pointer(&message)),
			0,
			0,
			0,
		)
		if int32(result) == -1 {
			return fmt.Errorf("读取安装进度消息: %w", err)
		}
		if result == 0 {
			break
		}
		installTranslate.Call(uintptr(unsafe.Pointer(&message)))
		installDispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
	installUI.mu.Lock()
	defer installUI.mu.Unlock()
	return installUI.err
}

func registerInstallClass() {
	controls := initCommonControlsEx{
		Size:    uint32(unsafe.Sizeof(initCommonControlsEx{})),
		Classes: installICCProgress,
	}
	result, _, err := installInitControls.Call(uintptr(unsafe.Pointer(&controls)))
	if result == 0 {
		installClassErr = fmt.Errorf("初始化安装进度控件: %w", err)
		return
	}
	cursor, _, _ := installLoadCursor.Call(0, installIDCArrow)
	class := installWindowClassEx{
		Size:       uint32(unsafe.Sizeof(installWindowClassEx{})),
		WindowProc: windows.NewCallback(installWindowProc),
		Cursor:     cursor,
		Background: installColorWindow + 1,
		ClassName:  installUTF16(installWindowClass),
	}
	atom, _, err := installRegisterClass.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		installClassErr = fmt.Errorf("注册安装进度窗口: %w", err)
	}
}

func installWindowProc(window uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case installWMCreate:
		createInstallControls(window)
		return 0
	case installWMProgress:
		updateInstallControls()
		return 0
	case installWMComplete:
		updateInstallControls()
		installDestroyWindow.Call(window)
		return 0
	case installWMClose:
		installSetControlText(installUI.status, "安装正在进行，请保持此窗口打开…")
		return 0
	case installWMDestroy:
		installPostQuit.Call(0)
		return 0
	}
	result, _, _ := installDefWindowProc.Call(window, uintptr(message), wParam, lParam)
	return result
}

func createInstallControls(window uintptr) {
	font, _, _ := installGetStock.Call(installDefaultFont)
	installCreateLabel(
		window,
		"EZGameLAN（OpenVPN/TAP 按需安装）",
		24, 20, 500, 24, font,
	)
	installUI.status = installCreateLabel(
		window,
		"正在准备安装…",
		24, 59, 500, 24, font,
	)
	installUI.bar = installCreateControl(
		"msctls_progress32",
		"",
		installWSChild|installWSVisible|installPBSmooth,
		24, 91, 500, 24,
		window, font,
	)
	installSendMessage.Call(installUI.bar, installPBMSetRange32, 0, 100)
	installUI.percent = installCreateLabel(
		window,
		"0%",
		474, 122, 50, 22, font,
	)
	installCreateLabel(
		window,
		"需要安装驱动时可能出现 Windows 安全确认，请按提示继续。",
		24, 145, 450, 22, font,
	)
}

func reportInstallProgress(progress setup.Progress) {
	if progress.Stage == "" {
		return
	}
	installUI.mu.Lock()
	if progress.Percent >= installUI.current.Percent {
		installUI.current = progress
	}
	installUI.mu.Unlock()
	installPostMessage.Call(installUI.window, installWMProgress, 0, 0)
}

func updateInstallControls() {
	installUI.mu.Lock()
	progress := installUI.current
	installUI.mu.Unlock()
	if progress.Stage == "" {
		return
	}
	installSetControlText(installUI.status, progress.Message)
	installSetControlText(installUI.percent, fmt.Sprintf("%d%%", progress.Percent))
	installSendMessage.Call(
		installUI.bar,
		installPBMSetPos,
		uintptr(progress.Percent),
		0,
	)
}

func installCreateLabel(
	parent uintptr,
	text string,
	x, y, width, height int32,
	font uintptr,
) uintptr {
	return installCreateControl(
		"STATIC",
		text,
		installWSChild|installWSVisible,
		x, y, width, height,
		parent, font,
	)
}

func installCreateControl(
	class, text string,
	style uint32,
	x, y, width, height int32,
	parent uintptr,
	font uintptr,
) uintptr {
	control, _, _ := installCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(installUTF16(class))),
		uintptr(unsafe.Pointer(installUTF16(text))),
		uintptr(style),
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		parent,
		0,
		0,
		0,
	)
	if control != 0 {
		installSendMessage.Call(control, installWMSetFont, font, 1)
	}
	return control
}

func installSetControlText(control uintptr, value string) {
	installSetText.Call(control, uintptr(unsafe.Pointer(installUTF16(value))))
}

func installUTF16(value string) *uint16 {
	pointer, _ := windows.UTF16PtrFromString(value)
	return pointer
}
