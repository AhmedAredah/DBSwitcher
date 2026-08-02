# DBSwitcher - MariaDB Configuration Manager

[![Build Status](https://github.com/AhmedAredah/MariaDBSwitcher/workflows/Build/badge.svg)](https://github.com/AhmedAredah/MariaDBSwitcher/actions)
[![Release](https://img.shields.io/github/v/release/AhmedAredah/MariaDBSwitcher)](https://github.com/AhmedAredah/MariaDBSwitcher/releases)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-%3E%3D%201.19-blue.svg)](https://golang.org/)

A powerful, cross-platform tool for managing multiple MariaDB/MySQL configurations with an intuitive GUI and comprehensive CLI interface.

![DBSwitcher Screenshot](docs/screenshot.png)

## Features

### Configuration Management

- **Multiple Configurations**: Manage unlimited MariaDB configurations
- **Easy Switching**: Switch between configurations with a single command/click
- **Auto-Detection**: Automatically detects existing MariaDB installations
- **Configuration Validation**: Validates configurations before starting

### Multiple Interfaces

- **GUI Application**: Modern, user-friendly graphical interface
- **Command Line Interface**: Full-featured CLI for automation and scripts
- **System Tray**: Lightweight system tray integration for quick access
- **Cross-Platform**: Windows, Linux, and macOS support

### Security & Credentials

- **Per-Configuration Credentials**: Each configuration has its own data directory and therefore its own accounts, so credentials are stored per configuration (`mysql_credentials:<config>` in the keyring) rather than shared
- **Secure Storage**: Credentials stored safely using system keyring, and only after they are known to work
- **Automatic Re-prompt**: If the stored password is rejected, you are asked for the current one and offered to update that configuration's keyring entry
- **No Password on the Command Line**: Credentials reach the client through a private options file, never through argv or the log
- **Flexible Authentication**: Support for password and passwordless connections
- **Session Management**: Remember credentials for the current session
- **Connection Testing**: Built-in connection validation

### Advanced Features

- **Real-time Monitoring**: Live status updates and process monitoring
- **Port Detection**: Intelligent port detection using multiple methods
- **Process Management**: Safe start/stop with proper cleanup
- **Logging**: Comprehensive logging for troubleshooting
- **Configuration Editor**: Built-in editor integration

## Installation

### Download Pre-built Binaries

Download the latest release for your platform from the [Releases](https://github.com/AhmedAredah/MariaDBSwitcher/releases) page:

- **Windows**: `dbswitcher-windows-amd64.exe`
- **Linux**: `dbswitcher-linux-amd64`
- **macOS**: `dbswitcher-darwin-amd64`

### Build from Source

```bash
# Clone the repository
git clone https://github.com/AhmedAredah/MariaDBSwitcher.git
cd MariaDBSwitcher

# Build for your platform
go build -ldflags "-s -w" -o dbswitcher main.go

# Or build for all platforms
make build-all
```

### Requirements

- Go 1.19 or later (for building from source)
- MariaDB/MySQL installation
- GTK3 development libraries (Linux GUI)

## Quick Start

### GUI Mode (Default)

```bash
# Launch GUI
./dbswitcher
# or explicitly
./dbswitcher gui
```

### Command Line Usage

```bash
# List available configurations
./dbswitcher list

# Check current status
./dbswitcher status

# Start with a specific configuration
./dbswitcher start production

# Switch to another configuration
./dbswitcher switch development

# Stop MariaDB
./dbswitcher stop

# Run in system tray
./dbswitcher tray

# Show help
./dbswitcher help
```

## Configuration

### Configuration Directory

DBSwitcher stores configurations in platform-specific directories:

- **Windows**: `%APPDATA%\DBSwitcher\configs`
- **Linux**: `~/.config/DBSwitcher`
- **macOS**: `~/Library/Application Support/DBSwitcher`

### Configuration File Format

Create `.ini` or `.cnf` files in the configuration directory:

```ini
[mysqld]
# Basic settings
port = 3306
datadir = /path/to/data/directory

# Optional: Custom socket (Unix systems)
socket = /path/to/mysql.sock

# Optional: Additional MariaDB settings
innodb_buffer_pool_size = 128M
max_connections = 100

# DBSwitcher metadata (optional)
[dbswitcher]
description = "Production Database Server"
```

### Example Configurations

#### Production Configuration (`production.ini`)

```ini
[mysqld]
port = 3306
datadir = /var/lib/mysql/production
innodb_buffer_pool_size = 1G
max_connections = 200

[dbswitcher]
description = "Production server with optimized settings"
```

#### Development Configuration (`development.ini`)

```ini
[mysqld]
port = 3307
datadir = /var/lib/mysql/development
innodb_buffer_pool_size = 256M
max_connections = 50

[dbswitcher]
description = "Development server for testing"
```

## Interface Guide

### GUI Features

- **Status Dashboard**: Real-time MariaDB status and configuration info
- **Quick Actions**: Start/stop with dropdown configuration selection
- **Configuration Manager**: Full configuration management with editing
- **System Tray**: Optional system tray mode with quick access menu
- **Settings**: Appearance customization and credential management

### CLI Commands

| Command | Description | Example |
|---------|-------------|---------|
| `list` | Show all configurations | `dbswitcher list` |
| `status` | Display current MariaDB status | `dbswitcher status` |
| `start <config>` | Start with specified configuration | `dbswitcher start production` |
| `switch <config>` | Switch to different configuration | `dbswitcher switch development` |
| `stop` | Stop running MariaDB instance | `dbswitcher stop` |
| `gui` | Launch graphical interface | `dbswitcher gui` |
| `tray` | Run in system tray mode | `dbswitcher tray` |
| `version` | Show version information | `dbswitcher version` |
| `help` | Display help information | `dbswitcher help` |

### System Tray Menu

- **Show**: Open main window
- **Status**: Quick status dialog
- **Start with Config**: Dynamic menu of available configurations
- **Stop MariaDB**: Stop current instance
- **Settings/Logs/About**: Quick access to utilities
- **Exit**: Close application

## Advanced Usage

### Environment Variables

```bash
# Custom configuration directory
export DBSWITCHER_CONFIG_DIR="/custom/config/path"

# Custom MariaDB binary path
export DBSWITCHER_MARIADB_BIN="/custom/mariadb/bin"

# Enable debug logging
export DBSWITCHER_DEBUG=1
```

### Automation & Scripting

```bash
#!/bin/bash
# Example: Automated database switching script

# Switch to maintenance configuration
./dbswitcher switch maintenance

# Run maintenance tasks
echo "Running maintenance..."
sleep 10

# Switch back to production
./dbswitcher switch production

echo "Maintenance completed"
```

### Integration with CI/CD

```yaml
# Example GitHub Actions step
- name: Switch to test database
  run: |
    dbswitcher stop
    dbswitcher start testing
    dbswitcher status
```

## Development

### Architecture

```bash
MariaDBSwitcher/
├── core/           # Business logic and data management
│   ├── config.go   # Configuration management
│   ├── mariadb.go  # MariaDB operations
│   ├── credentials.go # Credential management
│   └── ...
├── gui/            # Graphical user interface
│   ├── app.go      # Main application
│   ├── statuscard.go # Status monitoring
│   ├── configcard.go # Configuration management
│   └── ...
├── cli/            # Command-line interface
│   └── commands.go # CLI command implementations
└── main.go         # Application entry point
```

### Building

#### Development Build

```bash
go build -o dbswitcher main.go
```

#### Production Build

```bash
# With optimization
go build -ldflags "-s -w" -o dbswitcher main.go

# With version information
go build -ldflags "-s -w -X main.Version=0.0.1" -o dbswitcher main.go
```

#### Cross-Platform Build

```bash
# Windows
GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o dbswitcher-windows-amd64.exe main.go

# Linux
GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o dbswitcher-linux-amd64 main.go

# macOS
GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w" -o dbswitcher-darwin-amd64 main.go
```

### Dependencies

- **[fyne.io/fyne/v2](https://fyne.io/)** - GUI framework
- **[getlantern/systray](https://github.com/getlantern/systray)** - System tray support
- **[zalando/go-keyring](https://github.com/zalando/go-keyring)** - Secure credential storage

## System Requirements

### Minimum Requirements

- **OS**: Windows 10, Linux (GTK3), macOS 10.14
- **Memory**: 64MB RAM
- **Storage**: 50MB available space
- **MariaDB/MySQL**: Any version with standard tools

### Supported Platforms

- Windows (10, 11)
- Linux (Ubuntu, Debian, CentOS, Fedora, Arch)
- macOS (10.14+)
- FreeBSD (experimental)

## Troubleshooting

### Common Issues

#### MariaDB Not Detected

```bash
# Check if MariaDB is in PATH
which mysqld

# Or specify custom path
export DBSWITCHER_MARIADB_BIN="/custom/path/to/mariadb/bin"
```

#### Configuration Not Found

```bash
# Check configuration directory
./dbswitcher list

# Verify file permissions
ls -la ~/.config/DBSwitcher/
```

#### Permission Issues

```bash
# Linux/macOS: Ensure proper permissions
sudo chown -R $USER ~/.config/DBSwitcher/

# Windows: Run as administrator if needed
```

### Logging

Check application logs for detailed troubleshooting:

- **Windows**: `%APPDATA%\DBSwitcher\dbswitcher.log`
- **Linux/macOS**: `~/.config/DBSwitcher/dbswitcher.log`

Enable debug logging:

```bash
export DBSWITCHER_DEBUG=1
./dbswitcher status
```

## 🤝 Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for details.

### Development Setup

```bash
# Clone and setup
git clone https://github.com/AhmedAredah/MariaDBSwitcher.git
cd MariaDBSwitcher

# Install dependencies
go mod tidy

# Run tests
go test ./...

# Run with development logging
go run main.go status
```

### Reporting Issues

Please use the [GitHub Issues](https://github.com/AhmedAredah/MariaDBSwitcher/issues) page to report bugs or request features.

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Author

**Ahmed Aredah**

- GitHub: [@AhmedAredah](https://github.com/AhmedAredah)
- Email: <Ahmed.Aredah@gmail.com>

## Acknowledgments

- [Fyne](https://fyne.io/) for the excellent GUI framework
- [Go](https://golang.org/) for the powerful programming language
- MariaDB/MySQL communities for the amazing database systems
- All contributors and users who help improve this tool

---

⭐ **If you find DBSwitcher useful, please consider giving it a star on GitHub!**

📥 **Download the latest release**: [GitHub Releases](https://github.com/AhmedAredah/MariaDBSwitcher/releases)
