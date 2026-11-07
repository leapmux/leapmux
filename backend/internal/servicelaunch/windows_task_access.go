package servicelaunch

const (
	windowsTaskCreateOrUpdate      = 0x6
	windowsTaskDontAddPrincipalACE = 0x10
	windowsTaskRegistrationFlags   = windowsTaskCreateOrUpdate | windowsTaskDontAddPrincipalACE

	// Task permissions use the file-object access mapping in winnt.h.
	windowsTaskFullAccess         uint32 = 0x001f01ff
	windowsTaskGenericRead        uint32 = 0x80000000
	windowsTaskGenericWrite       uint32 = 0x40000000
	windowsTaskGenericExecute     uint32 = 0x20000000
	windowsTaskGenericAll         uint32 = 0x10000000
	windowsTaskFileGenericRead    uint32 = 0x00120089
	windowsTaskFileGenericWrite   uint32 = 0x00120116
	windowsTaskFileGenericExecute uint32 = 0x001200a0
)

type windowsTaskAccessEntry struct {
	SID   string
	Kind  uint8
	Flags uint8
	Mask  uint32
}

type windowsTaskAccess struct {
	Present   bool
	Protected bool
	Defaulted bool
	Entries   []windowsTaskAccessEntry
}

func windowsTaskSecurityDescriptor(account string) string {
	return "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + account + ")"
}

func windowsTaskFileAccess(mask uint32) uint32 {
	mappings := [...]struct{ generic, specific uint32 }{
		{windowsTaskGenericAll, windowsTaskFullAccess},
		{windowsTaskGenericRead, windowsTaskFileGenericRead},
		{windowsTaskGenericWrite, windowsTaskFileGenericWrite},
		{windowsTaskGenericExecute, windowsTaskFileGenericExecute},
	}
	for _, mapping := range mappings {
		if mask&mapping.generic != 0 {
			mask = mask&^mapping.generic | mapping.specific
		}
	}
	return mask
}

func (access windowsTaskAccess) matches(account string) bool {
	if account == "" || !access.Present || !access.Protected || access.Defaulted {
		return false
	}
	expected := map[string]uint32{
		"S-1-5-18":     windowsTaskFullAccess,
		"S-1-5-32-544": windowsTaskFullAccess,
		account:        windowsTaskFullAccess,
	}
	granted := make(map[string]uint32, len(expected))
	for _, entry := range access.Entries {
		// Only explicit allow entries implement the installed policy.
		// This also rejects inherit-only, inherited, and object ACEs.
		if entry.Kind != 0 || entry.Flags != 0 {
			return false
		}
		allowed, exists := expected[entry.SID]
		mask := windowsTaskFileAccess(entry.Mask)
		if !exists || mask&^allowed != 0 {
			return false
		}
		granted[entry.SID] |= mask
	}
	for sid, mask := range expected {
		if granted[sid] != mask {
			return false
		}
	}
	return true
}
