#ifndef AppVersion
  #define AppVersion "1.3.1"
#endif
#ifndef SourceDir
  #error SourceDir must point to the staged Windows files
#endif
#ifndef GeoDir
  #error GeoDir must point to the DB-IP Lite files
#endif
#ifndef OutputDir
  #error OutputDir is required
#endif

[Setup]
AppId={{E829287E-EF1E-45A6-A825-2B67A7FB87C8}
AppName=PacketTrail
AppVersion={#AppVersion}
AppPublisher=Matthew Burnett
AppPublisherURL=https://github.com/mattbrianb/packettrail
AppSupportURL=https://github.com/mattbrianb/packettrail/issues
AppUpdatesURL=https://github.com/mattbrianb/packettrail/releases
DefaultDirName={localappdata}\Programs\PacketTrail
DefaultGroupName=PacketTrail
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir={#OutputDir}
OutputBaseFilename=packettrail-windows-x64-setup
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
UninstallDisplayIcon={app}\packettrail.exe
LicenseFile={#SourceDir}\LICENSE

[Files]
Source: "{#SourceDir}\packettrail.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\THIRD-PARTY-NOTICES.md"; DestDir: "{app}"; Flags: ignoreversion
; Never overwrite a user's existing or manually updated databases.
Source: "{#GeoDir}\dbip-city-lite.mmdb"; DestDir: "{userappdata}\packettrail"; Flags: onlyifdoesntexist uninsneveruninstall
Source: "{#GeoDir}\dbip-city-lite.version"; DestDir: "{userappdata}\packettrail"; Flags: onlyifdoesntexist uninsneveruninstall
Source: "{#GeoDir}\dbip-asn-lite.mmdb"; DestDir: "{userappdata}\packettrail"; Flags: onlyifdoesntexist uninsneveruninstall
Source: "{#GeoDir}\dbip-asn-lite.version"; DestDir: "{userappdata}\packettrail"; Flags: onlyifdoesntexist uninsneveruninstall

[Icons]
Name: "{group}\PacketTrail"; Filename: "{app}\packettrail.exe"
Name: "{autodesktop}\PacketTrail"; Filename: "{app}\packettrail.exe"; Tasks: desktopicon

[Tasks]
Name: desktopicon; Description: "Create a desktop shortcut"; GroupDescription: "Additional shortcuts:"; Flags: unchecked

[Run]
Filename: "{app}\packettrail.exe"; Description: "Launch PacketTrail"; Flags: nowait postinstall skipifsilent
