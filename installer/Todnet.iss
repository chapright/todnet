; Inno Setup script for Todnet.
;
; The app is a single self-contained exe that keeps its state in
; %APPDATA%\Todnet and asks for elevation only if the user switches to TUN
; mode. The installer defaults to a normal machine-wide install under
; %ProgramFiles%\Todnet, which costs one UAC prompt at install time. The
; install-mode page also offers "only for me", which installs under
; %LOCALAPPDATA%\Programs without elevation.
;
; Build with tools\build-release.ps1, or directly:
;   iscc /DAppVersion=1.0.0 installer\Todnet.iss
;
; The output lands in dist\Todnet-<version>-setup.exe.

#define AppName "Todnet"
#define AppExe "Todnet.exe"

; Stable identity for upgrades and the uninstall entry. Never change this.
#define AppId "{{6E2F1C2B-9D4A-4C57-A8F1-3B7E5D9C4A21}"

#ifndef AppVersion
  #define AppVersion "1.0.0"
#endif

[Setup]
AppId={#AppId}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=Todnet
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
DisableDirPage=auto
UsePreviousAppDir=yes
; {autopf} resolves to C:\Program Files\Todnet in the default (all users)
; install and to %LOCALAPPDATA%\Programs\Todnet in the "only for me" install.
PrivilegesRequired=admin
PrivilegesRequiredOverridesAllowed=dialog commandline
OutputDir=..\dist
OutputBaseFilename={#AppName}-{#AppVersion}-setup
SetupIconFile=..\web\icon.ico
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
; Close a running Todnet (it lives in the notification area) so an upgrade can
; replace the exe; Start Menu and desktop shortcuts are refreshed in place.
CloseApplications=yes
RestartApplications=no
VersionInfoVersion={#AppVersion}
VersionInfoTextVersion={#AppVersion}
VersionInfoCompany={#AppName}
VersionInfoDescription={#AppName} installer
VersionInfoProductName={#AppName}
VersionInfoProductVersion={#AppVersion}

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "..\dist\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{group}\{cm:UninstallProgram,{#AppName}}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExe}"; Description: "{cm:LaunchProgram,{#AppName}}"; Flags: nowait postinstall skipifsilent

; Settings, saved accounts and the window geometry live in %APPDATA%\Todnet
; and are deliberately left behind on uninstall, so reinstalling does not
; sign the user out or forget their choices. There is nothing to delete here.
