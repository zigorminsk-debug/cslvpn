; CSLVPN Windows installer (NSIS).
;
; Build (on Windows or with makensis from nsis on Linux):
;   makensis -DVERSION=0.2.0 -DSRC=build\cslvpn-windows-amd64.exe \
;            -DOUT=pkg\cslvpn-0.2.0-windows-amd64-setup.exe packaging\nsis\cslvpn.nsi
;
; Layout:
;   %ProgramFiles%\CSLVPN\cslvpn.exe
;   %ProgramData%\CSLVPN\server.env.example, client.env.example
;   Start Menu shortcuts for "Relay (console)" and "Client (console)"
;   Optional Windows service "cslvpn-relay" (relay, auto start)
;   Add/Remove Programs entry, clean uninstall (configs are kept)

Unicode true
!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef SRC
  !define SRC "cslvpn.exe"
!endif
!ifndef OUT
  !define OUT "cslvpn-setup.exe"
!endif
!ifndef ARCH
  !define ARCH "amd64"
!endif
; PROJECT is the repository root; CI passes it absolute because makensis
; resolves relative paths against this script's directory.
!ifndef PROJECT
  !define PROJECT "..\.."
!endif

!define APPNAME "CSLVPN"
!define COMPANY "CSLVPN contributors"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\CSLVPN"
!define SERVICE "cslvpn-relay"

Name "${APPNAME} ${VERSION} (${ARCH})"
OutFile "${OUT}"
InstallDir "$PROGRAMFILES64\CSLVPN"
InstallDirRegKey HKLM "Software\CSLVPN" "InstallDir"
RequestExecutionLevel admin
SetCompressor /SOLID lzma
BrandingText "${APPNAME} - isolated virtual LANs over UDP/TCP"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "CompanyName" "${COMPANY}"
VIAddVersionKey "FileDescription" "CSLVPN installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "MIT License"

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_SHOWREADME "$INSTDIR\SERVER.md"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${PROJECT}\LICENSE"
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "CSLVPN core (relay + client)" SEC_CORE
  SectionIn RO
  SetOutPath "$INSTDIR"
  File "/oname=cslvpn.exe" "${SRC}"
  File "${PROJECT}\LICENSE"
  File "${PROJECT}\README.md"
  File "${PROJECT}\docs\SERVER.md"
  File "${PROJECT}\docs\CLIENT.md"
  File "${PROJECT}\docs\WINDOWS.md"

  ; NSIS supports shipped languages only; keep the installer translatable.
  SetOutPath "$COMMONAPPDATA\CSLVPN"
  File "${PROJECT}\packaging\config\server.env.example"
  File "${PROJECT}\packaging\config\client.env.example"

  WriteRegStr HKLM "Software\CSLVPN" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\CSLVPN" "Version" "${VERSION}"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  ; Add/Remove Programs
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "${COMPANY}"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\cslvpn.exe"
  WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "${UNINST_KEY}" "EstimatedSize" "$0"

  ; Firewall rules for the relay (both transports share one port).
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="CSLVPN relay UDP"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="CSLVPN relay UDP" dir=in action=allow protocol=UDP localport=7777 profile=any'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="CSLVPN relay TCP"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="CSLVPN relay TCP" dir=in action=allow protocol=TCP localport=7777 profile=any'
SectionEnd

Section "Start menu shortcuts" SEC_SHORTCUTS
  CreateDirectory "$SMPROGRAMS\CSLVPN"
  CreateShortcut "$SMPROGRAMS\CSLVPN\CSLVPN relay.lnk" "$SYSDIR\cmd.exe" \
    '/k "$INSTDIR\cslvpn.exe" server -listen :7777 -admin 127.0.0.1:7778' \
    "$INSTDIR\cslvpn.exe" 0
  CreateShortcut "$SMPROGRAMS\CSLVPN\CSLVPN client.lnk" "$SYSDIR\cmd.exe" \
    '/k "$INSTDIR\cslvpn.exe" client -server 127.0.0.1:7777 -network office -password secret -transport tcp' \
    "$INSTDIR\cslvpn.exe" 0
  CreateShortcut "$SMPROGRAMS\CSLVPN\Uninstall.lnk" "$INSTDIR\uninstall.exe"
SectionEnd

Section "Windows service (relay, auto start)" SEC_SERVICE
  nsExec::ExecToLog 'sc.exe stop ${SERVICE}'
  nsExec::ExecToLog 'sc.exe delete ${SERVICE}'
  nsExec::ExecToLog 'sc.exe create ${SERVICE} binPath= "\"$INSTDIR\cslvpn.exe\" server -listen :7777 -admin 127.0.0.1:7778" start= auto DisplayName= "CSLVPN relay"'
  nsExec::ExecToLog 'sc.exe description ${SERVICE} "CSLVPN relay server (UDP and TCP on port 7777)"'
  nsExec::ExecToLog 'sc.exe failure ${SERVICE} reset= 86400 actions= restart/2000/restart/5000/restart/10000'
  nsExec::ExecToLog 'sc.exe start ${SERVICE}'
SectionEnd

Section "Desktop shortcuts"
  CreateShortcut "$DESKTOP\CSLVPN relay.lnk" "$SYSDIR\cmd.exe" \
    '/k "$INSTDIR\cslvpn.exe" server -listen :7777 -admin 127.0.0.1:7778' "$INSTDIR\cslvpn.exe" 0
SectionEnd

Section "Uninstall"
  nsExec::ExecToLog 'sc.exe stop ${SERVICE}'
  nsExec::ExecToLog 'sc.exe delete ${SERVICE}'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="CSLVPN relay UDP"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="CSLVPN relay TCP"'

  Delete "$INSTDIR\cslvpn.exe"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\SERVER.md"
  Delete "$INSTDIR\CLIENT.md"
  Delete "$INSTDIR\WINDOWS.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\CSLVPN\CSLVPN relay.lnk"
  Delete "$SMPROGRAMS\CSLVPN\CSLVPN client.lnk"
  Delete "$SMPROGRAMS\CSLVPN\Uninstall.lnk"
  RMDir "$SMPROGRAMS\CSLVPN"
  Delete "$DESKTOP\CSLVPN relay.lnk"

  DeleteRegKey HKLM "${UNINST_KEY}"
  DeleteRegKey HKLM "Software\CSLVPN"
  ; %ProgramData%\CSLVPN is intentionally kept (user configuration).
SectionEnd

LangString DESC_SEC_CORE ${LANG_ENGLISH} "cslvpn.exe (relay, client and status CLI) with documentation."
LangString DESC_SEC_SHORTCUTS ${LANG_ENGLISH} "Start menu shortcuts."
LangString DESC_SEC_SERVICE ${LANG_ENGLISH} "Install the relay as a Windows service that starts automatically."
LangString DESC_SEC_DESKTOP ${LANG_ENGLISH} "Desktop shortcut for the relay console."

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SEC_CORE} $(DESC_SEC_CORE)
  !insertmacro MUI_DESCRIPTION_TEXT ${SEC_SHORTCUTS} $(DESC_SEC_SHORTCUTS)
  !insertmacro MUI_DESCRIPTION_TEXT ${SEC_SERVICE} $(DESC_SEC_SERVICE)
  !insertmacro MUI_DESCRIPTION_TEXT ${SEC_DESKTOP} $(DESC_SEC_DESKTOP)
!insertmacro MUI_FUNCTION_DESCRIPTION_END
