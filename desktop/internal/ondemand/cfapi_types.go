package ondemand

// Structures of the Cloud Files API (cfapi.h) as the 64-bit Windows ABI
// lays them out (amd64, arm64). Pointers are uintptr so that the layout
// can be checked on any system (cfapi_types_test.go); only the fields this
// provider uses are named.

// CF_CALLBACK_INFO (152 bytes).
type cfCallbackInfo struct {
	StructSize          uint32
	_                   uint32
	ConnectionKey       int64
	CallbackContext     uintptr
	VolumeGuidName      uintptr // PCWSTR
	VolumeDosName       uintptr // PCWSTR ("C:")
	VolumeSerialNumber  uint32
	_                   uint32
	SyncRootFileID      int64
	SyncRootIdentity    uintptr
	SyncRootIdentityLen uint32
	_                   uint32
	FileID              int64
	FileSize            int64
	FileIdentity        uintptr
	FileIdentityLength  uint32
	_                   uint32
	NormalizedPath      uintptr // PCWSTR, from the root of the volume
	TransferKey         int64
	PriorityHint        uint8
	_                   [7]byte
	CorrelationVector   uintptr
	ProcessInfo         uintptr // CF_PROCESS_INFO*: StructSize, ProcessId…
	RequestKey          int64
}

// CF_CALLBACK_PARAMETERS: ParamSize, then a union aligned on 8 whose
// members all start with their Flags. A and B are the first two 64-bit
// fields: FetchData RequiredFileOffset/RequiredLength, Cancel
// FileOffset/Length, Rename TargetPath (A).
type cfCallbackParams struct {
	ParamSize uint32
	_         uint32
	Flags     uint32
	_         uint32
	A         int64
	B         int64
	C         int64
	D         int64
}

// CF_OPERATION_INFO (48 bytes).
type cfOperationInfo struct {
	StructSize        uint32
	Type              uint32
	ConnectionKey     int64
	TransferKey       int64
	CorrelationVector uintptr
	SyncStatus        uintptr
	RequestKey        int64
}

// CF_OPERATION_PARAMETERS (48 bytes): ParamSize, then the union. Used as
// TransferData (Flags, CompletionStatus, Buffer, Offset, Length; size 40)
// and AckDelete/AckRename (Flags, CompletionStatus; size 16).
type cfOperationParams struct {
	ParamSize        uint32
	_                uint32
	Flags            uint32
	CompletionStatus int32
	Buffer           uintptr
	Offset           int64
	Length           int64
	_                int64
}

// FILE_BASIC_INFO + file size: CF_FS_METADATA (48 bytes).
type cfFSMetadata struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
	FileSize       int64
}

// CF_PLACEHOLDER_CREATE_INFO (88 bytes).
type cfPlaceholderCreateInfo struct {
	RelativeFileName   uintptr // PCWSTR
	FsMetadata         cfFSMetadata
	FileIdentity       uintptr
	FileIdentityLength uint32
	Flags              uint32
	Result             int32
	_                  uint32
	CreateUsn          int64
}

// CF_SYNC_REGISTRATION (72 bytes).
type cfSyncRegistration struct {
	StructSize             uint32
	_                      uint32
	ProviderName           uintptr
	ProviderVersion        uintptr
	SyncRootIdentity       uintptr
	SyncRootIdentityLength uint32
	_                      uint32
	FileIdentity           uintptr
	FileIdentityLength     uint32
	ProviderID             [16]byte // GUID
	_                      uint32
}

// CF_SYNC_POLICIES (24 bytes).
type cfSyncPolicies struct {
	StructSize            uint32
	HydrationPrimary      uint16
	HydrationModifier     uint16
	PopulationPrimary     uint16
	PopulationModifier    uint16
	InSync                uint32
	HardLink              uint32
	PlaceholderManagement uint32
}

// CF_CALLBACK_REGISTRATION (16 bytes).
type cfCallbackRegistration struct {
	Type     uint32
	_        uint32
	Callback uintptr
}

// WIN32_FIND_DATAW (592 bytes): FindFirstFileW's, as
// CfGetPlaceholderStateFromFindData reads it (x/sys/windows converts its own).
type win32FindData struct {
	FileAttributes    uint32
	CreationTime      [2]uint32
	LastAccessTime    [2]uint32
	LastWriteTime     [2]uint32
	FileSizeHigh      uint32
	FileSizeLow       uint32
	Reserved0         uint32
	Reserved1         uint32
	FileName          [260]uint16
	AlternateFileName [14]uint16
}

// Constants of cfapi.h used here.
const (
	cfCallbackFetchData       = 0
	cfCallbackCancelFetchData = 2
	cfCallbackNotifyDelete    = 9
	cfCallbackNotifyRename    = 11
	cfCallbackNone            = 0xffffffff

	cfOpTransferData = 0
	cfOpAckDelete    = 6
	cfOpAckRename    = 7

	cfConnectRequireProcessInfo  = 2
	cfConnectRequireFullFilePath = 4

	cfPlaceholderDisableOnDemandPopulation = 1
	cfPlaceholderMarkInSync                = 2

	cfRenameFlagSourceInScope = 2
	cfRenameFlagTargetInScope = 4

	cfUpdateMarkInSync  = 2
	cfUpdateDehydrate   = 4
	cfConvertMarkInSync = 1

	cfPinPinned   = 1
	cfPinUnpinned = 2
	cfPinRecurse  = 1

	cfInSync = 1

	cfStatePlaceholder = 1
	cfStateInSync      = 8
	cfStateInvalid     = 0xffffffff

	cfHydrationFull          = 2
	cfPopulationAlwaysFull   = 3
	cfRegisterFlagUpdate     = 1
	cfRegisterMarkInSyncRoot = 4

	// File attributes.
	attrReadonly           = 0x1
	attrDirectory          = 0x10
	attrNormal             = 0x80
	attrPinned             = 0x80000
	attrUnpinned           = 0x100000
	attrRecallOnDataAccess = 0x400000

	// NTSTATUS answered to the platform.
	statusSuccess          = 0
	statusUnsuccessful     = -0x3fffffff // 0xC0000001
	statusAccessDenied     = -0x3fffffde // 0xC0000022
	statusNotSameDevice    = -0x3fffff2c // 0xC00000D4
	statusCloudUnavailable = -0x3fff30f9 // 0xC000CF07 STATUS_CLOUD_FILE_NETWORK_UNAVAILABLE
)

// fileTime converts Unix milliseconds to a FILETIME (100 ns since 1601).
func fileTime(ms int64) int64 { return ms*10000 + 116444736000000000 }
