#ifndef MyVersion
  #error MyVersion is required
#endif
#ifndef MyArch
  #error MyArch is required
#endif
#ifndef SourceDir
  #error SourceDir is required
#endif
#ifndef OutputDir
  #error OutputDir is required
#endif

#if MyArch == "arm64"
  #define AllowedArch "arm64"
#else
  #define AllowedArch "x64compatible"
#endif

[Setup]
AppId={{B6B6C737-94EB-49CD-A0BB-3BC41318621A}
AppName=DeskPatrol
AppVersion={#MyVersion}
AppPublisher=DeskPatrol
DefaultDirName={autopf}\DeskPatrol
DisableProgramGroupPage=yes
ArchitecturesAllowed={#AllowedArch}
PrivilegesRequired=admin
OutputDir={#OutputDir}
OutputBaseFilename=DeskPatrol-{#MyVersion}-windows-{#MyArch}
Compression=lzma2
SolidCompression=yes
UninstallDisplayName=DeskPatrol
WizardStyle=modern
SetupLogging=yes

[Files]
Source: "{#SourceDir}\DeskPatrol.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\DeskPatrolHelper.exe"; DestDir: "{app}"; Flags: ignoreversion

[UninstallRun]
Filename: "{app}\MeshAgent.exe"; Parameters: "-fulluninstall"; Flags: runhidden waituntilterminated skipifdoesntexist

[Code]
function InitializeSetup(): Boolean;
begin
  Result := True;
end;
