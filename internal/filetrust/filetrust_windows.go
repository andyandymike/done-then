//go:build windows

package filetrust

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	tokenQuery                = 0x0008
	tokenUserClass            = 1
	seFileObject              = 1
	ownerSecurityInformation  = 0x00000001
	daclSecurityInformation   = 0x00000004
	errorInsufficientBuffer   = syscall.Errno(122)
	accessAllowedACEType      = 0
	accessDeniedACEType       = 1
	accessDeniedObjectACE     = 6
	accessDeniedCallbackACE   = 10
	winLocalSystemSID         = 22
	winBuiltinAdminsSID       = 26
	securityMaxSIDSize        = 68
	fileAttributeReparsePoint = 0x00000400
	invalidFileAttributes     = 0xffffffff
	writeAccessMask           = 0x10000000 | 0x40000000 | 0x00010000 | 0x00040000 | 0x00080000 |
		0x00000002 | 0x00000004 | 0x00000010 | 0x00000100
)

var (
	advapiFileTrust          = syscall.NewLazyDLL("advapi32.dll")
	kernel32FileTrust        = syscall.NewLazyDLL("kernel32.dll")
	openProcessTokenProc     = advapiFileTrust.NewProc("OpenProcessToken")
	getTokenInformationProc  = advapiFileTrust.NewProc("GetTokenInformation")
	getNamedSecurityInfoProc = advapiFileTrust.NewProc("GetNamedSecurityInfoW")
	getACEProc               = advapiFileTrust.NewProc("GetAce")
	equalSIDProc             = advapiFileTrust.NewProc("EqualSid")
	createWellKnownSIDProc   = advapiFileTrust.NewProc("CreateWellKnownSid")
	convertSIDToStringProc   = advapiFileTrust.NewProc("ConvertSidToStringSidW")
	convertSDDLProc          = advapiFileTrust.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	setFileSecurityProc      = advapiFileTrust.NewProc("SetFileSecurityW")
	getCurrentProcessProc    = kernel32FileTrust.NewProc("GetCurrentProcess")
	getFileAttributesProc    = kernel32FileTrust.NewProc("GetFileAttributesW")
	localFreeProc            = kernel32FileTrust.NewProc("LocalFree")
)

type sidAndAttributes struct {
	SID        uintptr
	Attributes uint32
}

type tokenUser struct {
	User sidAndAttributes
}

type aclHeader struct {
	Revision byte
	Sbz1     byte
	Size     uint16
	ACECount uint16
	Sbz2     uint16
}

type aceHeader struct {
	Type  byte
	Flags byte
	Size  uint16
}

func validateOwnerControlled(path string, info os.FileInfo, label string) error {
	return validateWindowsACL(path, label)
}

func validateOwnerControlledDirectory(path string, info os.FileInfo, label string) error {
	return validateWindowsACL(path, label)
}

func validateWindowsACL(path, label string) error {
	if err := rejectWindowsReparsePoint(path, label); err != nil {
		return err
	}
	currentSID, tokenHandle, currentSIDBuffer, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("inspect %s owner: %w", label, err)
	}
	defer syscall.CloseHandle(tokenHandle)
	defer runtime.KeepAlive(currentSIDBuffer)
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var ownerSID, dacl unsafe.Pointer
	var descriptor uintptr
	code, _, _ := getNamedSecurityInfoProc.Call(
		uintptr(unsafe.Pointer(pathUTF16)), seFileObject,
		ownerSecurityInformation|daclSecurityInformation,
		uintptr(unsafe.Pointer(&ownerSID)), 0, uintptr(unsafe.Pointer(&dacl)), 0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if descriptor != 0 {
		defer localFreeProc.Call(descriptor)
	}
	if code != 0 {
		return fmt.Errorf("read %s ACL: %w", label, syscall.Errno(code))
	}
	if ownerSID == nil || !equalSID(uintptr(ownerSID), currentSID) {
		return fmt.Errorf("%s must be owned by the current user", label)
	}
	if dacl == nil {
		return fmt.Errorf("%s has no restrictive DACL", label)
	}
	systemSIDBuffer, err := wellKnownSID(winLocalSystemSID)
	if err != nil {
		return err
	}
	adminsSIDBuffer, err := wellKnownSID(winBuiltinAdminsSID)
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(systemSIDBuffer)
	defer runtime.KeepAlive(adminsSIDBuffer)
	systemSID := uintptr(unsafe.Pointer(&systemSIDBuffer[0]))
	adminsSID := uintptr(unsafe.Pointer(&adminsSIDBuffer[0]))
	acl := (*aclHeader)(dacl)
	for index := uint16(0); index < acl.ACECount; index++ {
		var ace unsafe.Pointer
		result, _, callErr := getACEProc.Call(uintptr(dacl), uintptr(index), uintptr(unsafe.Pointer(&ace)))
		if result == 0 || ace == nil {
			return fmt.Errorf("read %s ACL entry: %w", label, callErr)
		}
		header := (*aceHeader)(ace)
		if header.Size < 8 {
			return fmt.Errorf("%s contains a malformed ACL entry", label)
		}
		if header.Type == accessDeniedACEType || header.Type == accessDeniedObjectACE || header.Type == accessDeniedCallbackACE {
			continue
		}
		mask := *(*uint32)(unsafe.Add(ace, 4))
		if mask&writeAccessMask == 0 {
			continue
		}
		if header.Type != accessAllowedACEType {
			return fmt.Errorf("%s contains an unsupported writable ACL entry", label)
		}
		sid := uintptr(unsafe.Add(ace, 8))
		if !equalSID(sid, currentSID) && !equalSID(sid, systemSID) && !equalSID(sid, adminsSID) {
			return fmt.Errorf("%s is writable by an identity other than the owner, SYSTEM, or Administrators", label)
		}
	}
	return nil
}

func currentUserSID() (uintptr, syscall.Handle, []byte, error) {
	process, _, _ := getCurrentProcessProc.Call()
	var token syscall.Handle
	result, _, callErr := openProcessTokenProc.Call(process, tokenQuery, uintptr(unsafe.Pointer(&token)))
	if result == 0 {
		return 0, 0, nil, callErr
	}
	var size uint32
	result, _, firstErr := getTokenInformationProc.Call(uintptr(token), tokenUserClass, 0, 0, uintptr(unsafe.Pointer(&size)))
	if result != 0 || !errors.Is(firstErr, errorInsufficientBuffer) || size == 0 {
		_ = syscall.CloseHandle(token)
		return 0, 0, nil, fmt.Errorf("query token user size: %w", firstErr)
	}
	buffer := make([]byte, size)
	result, _, callErr = getTokenInformationProc.Call(
		uintptr(token), tokenUserClass, uintptr(unsafe.Pointer(&buffer[0])), uintptr(size), uintptr(unsafe.Pointer(&size)),
	)
	if result == 0 {
		_ = syscall.CloseHandle(token)
		return 0, 0, nil, callErr
	}
	user := (*tokenUser)(unsafe.Pointer(&buffer[0]))
	return user.User.SID, token, buffer, nil
}

func wellKnownSID(kind uintptr) ([]byte, error) {
	buffer := make([]byte, securityMaxSIDSize)
	size := uint32(len(buffer))
	result, _, callErr := createWellKnownSIDProc.Call(kind, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if result == 0 {
		return nil, callErr
	}
	return buffer[:size], nil
}

func equalSID(left, right uintptr) bool {
	if left == 0 || right == 0 {
		return false
	}
	result, _, _ := equalSIDProc.Call(left, right)
	return result != 0
}

func hardenOwnerControlled(path string) error {
	return hardenWindowsACL(path)
}

func hardenOwnerControlledDirectory(path string) error {
	return hardenWindowsACL(path)
}

func hardenWindowsACL(path string) error {
	if err := rejectWindowsReparsePoint(path, "owner-controlled path"); err != nil {
		return err
	}
	currentSID, token, buffer, err := currentUserSID()
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(token)
	defer runtime.KeepAlive(buffer)
	sid, err := sidString(currentSID)
	if err != nil {
		return err
	}
	sddl, err := syscall.UTF16PtrFromString("D:P(A;;FA;;;" + sid + ")(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return err
	}
	var descriptor uintptr
	var descriptorSize uint32
	result, _, callErr := convertSDDLProc.Call(
		uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), uintptr(unsafe.Pointer(&descriptorSize)),
	)
	if result == 0 {
		return fmt.Errorf("build owner-only DACL: %w", callErr)
	}
	defer localFreeProc.Call(descriptor)
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	result, _, callErr = setFileSecurityProc.Call(uintptr(unsafe.Pointer(pathUTF16)), daclSecurityInformation, descriptor)
	if result == 0 {
		return fmt.Errorf("apply owner-only DACL: %w", callErr)
	}
	return nil
}

func rejectWindowsReparsePoint(path, label string) error {
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, _, callErr := getFileAttributesProc.Call(uintptr(unsafe.Pointer(pathUTF16)))
	if uint32(attributes) == invalidFileAttributes {
		return fmt.Errorf("inspect %s attributes: %w", label, callErr)
	}
	if uint32(attributes)&fileAttributeReparsePoint != 0 {
		return fmt.Errorf("%s must not be a Windows reparse point", label)
	}
	return nil
}

func sidString(sid uintptr) (string, error) {
	var value unsafe.Pointer
	result, _, callErr := convertSIDToStringProc.Call(sid, uintptr(unsafe.Pointer(&value)))
	if result == 0 || value == nil {
		return "", callErr
	}
	defer localFreeProc.Call(uintptr(value))
	length := 0
	for {
		character := *(*uint16)(unsafe.Add(value, length*2))
		if character == 0 {
			break
		}
		length++
		if length > 256 {
			return "", errors.New("Windows SID string is unexpectedly long")
		}
	}
	characters := unsafe.Slice((*uint16)(value), length)
	return syscall.UTF16ToString(characters), nil
}
