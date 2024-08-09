package tui

import "github.com/charmbracelet/lipgloss"

var (
	TitleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("098")).Bold(true)
	VersionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("060"))
	ErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
)
