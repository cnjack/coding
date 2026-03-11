# TUI Component Refactoring Plan

## 1. Goal
Split the monolithic `tui.go` (a 2000+ line file) into an elegant, maintainable component-based architecture inspired by modern frontend frameworks like Vue/React, while adhering to the `tea.Model` pattern in Bubble Tea.

## 2. Component Architecture Design

Just like in Vue where a large page is broken down into `<Header>`, `<ChatView>`, `<InputArea>`, `<Modals>`, etc., we will break the `tui.go` model down into the following nested models / separated files under `internal/tui/components` (or just split within `internal/tui/`). 

### Core Components
- **`App` (Main Model)** (`tui.go`): Acts as the main router and state holder. Holds all the child components. It receives external messages (channels) and passes them down to the active active component.
- **`Chat` Component** (`chat.go`): Handles the conversation view, including `viewport`, `markdown rendering`, and the `thinking` spinner. Responsible for rendering agent/user dialogs and tool results.
- **`Input` Component** (`input.go`): Wraps the `textarea`, handles multiline input, history tracking (up/down arrows), and `todoStore` display. Communicates events up using custom messages (e.g., `PromptSubmitMsg`).
- **`StatusBar` Component** (`statusbar.go`): Renders the bottom information bar (current model, tokens, auto-approve, MCP status). Receives states via Update and renders them cleanly.
- **`Modals / Pickers`**:
  - **`SettingMenu`** (`setting.go`)
  - **`ModelPicker`** (`modelPicker.go`)
  - **`SSHPicker`** (`sshPicker.go` & `dirPicker.go`)
  - **`SessionPicker`** (`sessionPicker.go`)
  - **`ApprovalDialog`** (`approval.go`)

## 3. Communication Mechanism (Props & Events)

In Vue, components communicate via Props (parent to child) and Events (child to parent).
In Bubble Tea, we achieve this via:
- **Props**: Parent passes data down by calling `m.component.Update(msg)` or updating fields directly.
- **Events**: Child returns a `tea.Cmd` that yields a specific `tea.Msg` (e.g., `return msg`). The Main Model matches this `tea.Msg` in its `Update(msg)` and acts accordingly.

## 4. Implementation Steps

1. **Extract Constants & Messages** (`messages.go` / `types.go`): Move all the isolated struct definitions (`AgentTextMsg`, `ToolCallMsg`, etc.) into a shared file to avoid cyclic dependencies.
2. **Create Component Files**: Build out the structs that conform to `tea.Model` for each logical block mentioned above.
3. **Refactor `tui.go` `Update` method**: Instead of a 500-line switch case, `tui.go` will delegate `tea.KeyMsg` to the currently focused component and handle global state `tea.Msg`.
4. **Refactor `tui.go` `View` method**: It will simply assemble the views:
   ```go
   func (m AppModel) View() string {
       if m.activeModal != nil { return m.activeModal.View() }
       return lipgloss.JoinVertical(lipgloss.Left, HeaderView(), m.chat.View(), m.input.View(), m.statusbar.View())
   }
   ```
5. **Ensure backward compatibility** with the channels and `main.go`.
