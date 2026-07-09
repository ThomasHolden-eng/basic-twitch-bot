package command

// RegisterCommands is the entry point for adding chat commands.
//
// Each command is a CommandFunc with signature
//
//	func(user chat.User, args []string) (string, error)
//
// and is registered with h.Register("name", fn). Implementations belong
// here in follow-up work; today the framework is the deliverable, not
// the command set.
func RegisterCommands(h *CommandHandler) {
}
