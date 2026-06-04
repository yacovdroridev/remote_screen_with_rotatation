; Antigravity Remote Viewer — Windows Installer
; Built with NSIS (Nullsoft Scriptable Install System)

Unicode True

;-----------------------------------------------------------------------
; General
;-----------------------------------------------------------------------
!define APP_NAME      "Antigravity Remote Viewer"
!define APP_EXE       "remote_viewer.exe"
!define APP_VERSION   "2.0.0"
!define PUBLISHER     "Antigravity Team"
!define INSTALL_DIR   "$PROGRAMFILES64\${APP_NAME}"
!define REG_UNINSTALL "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}"

Name              "${APP_NAME}"
OutFile           "AntigravityRemoteViewer-Setup.exe"
InstallDir        "${INSTALL_DIR}"
InstallDirRegKey  HKLM "${REG_UNINSTALL}" "InstallLocation"
RequestExecutionLevel admin
SetCompressor     /SOLID lzma
BrandingText      "${APP_NAME} v${APP_VERSION}"

;-----------------------------------------------------------------------
; Modern UI
;-----------------------------------------------------------------------
!include "MUI2.nsh"

!define MUI_ABORTWARNING
!define MUI_ICON   "${NSISDIR}\Contrib\Graphics\Icons\nsis3-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\nsis3-uninstall.ico"

; Welcome page
!define MUI_WELCOMEPAGE_TITLE    "Welcome to ${APP_NAME} Setup"
!define MUI_WELCOMEPAGE_TEXT     "This wizard will guide you through the installation of ${APP_NAME} v${APP_VERSION}.$\r$\n$\r$\nA high-performance SSH remote desktop viewer with GPU-accelerated rotation and input forwarding.$\r$\n$\r$\nClick Next to continue."
!insertmacro MUI_PAGE_WELCOME

; License page
!insertmacro MUI_PAGE_LICENSE "LICENSE"

; Directory page
!insertmacro MUI_PAGE_DIRECTORY

; Install files page
!insertmacro MUI_PAGE_INSTFILES

; Finish page — offer to launch immediately
!define MUI_FINISHPAGE_RUN         "$INSTDIR\${APP_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT    "Launch ${APP_NAME} now"
!define MUI_FINISHPAGE_SHOWREADME  ""
!insertmacro MUI_PAGE_FINISH

; Uninstaller pages
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

;-----------------------------------------------------------------------
; Install section
;-----------------------------------------------------------------------
Section "Application" SecApp
    SectionIn RO  ; Required — cannot be deselected

    SetOutPath "$INSTDIR"
    File "remote_viewer.exe"

    ; Write uninstaller
    WriteUninstaller "$INSTDIR\Uninstall.exe"

    ; Registry entries for Add/Remove Programs
    WriteRegStr   HKLM "${REG_UNINSTALL}" "DisplayName"      "${APP_NAME}"
    WriteRegStr   HKLM "${REG_UNINSTALL}" "DisplayVersion"   "${APP_VERSION}"
    WriteRegStr   HKLM "${REG_UNINSTALL}" "Publisher"        "${PUBLISHER}"
    WriteRegStr   HKLM "${REG_UNINSTALL}" "InstallLocation"  "$INSTDIR"
    WriteRegStr   HKLM "${REG_UNINSTALL}" "UninstallString"  '"$INSTDIR\Uninstall.exe"'
    WriteRegDWORD HKLM "${REG_UNINSTALL}" "NoModify"         1
    WriteRegDWORD HKLM "${REG_UNINSTALL}" "NoRepair"         1

    ; Start Menu shortcut
    CreateDirectory "$SMPROGRAMS\${APP_NAME}"
    CreateShortcut  "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" \
                    "$INSTDIR\${APP_EXE}" "" "$INSTDIR\${APP_EXE}" 0

    CreateShortcut  "$SMPROGRAMS\${APP_NAME}\Uninstall.lnk" \
                    "$INSTDIR\Uninstall.exe"

    ; Desktop shortcut
    CreateShortcut "$DESKTOP\${APP_NAME}.lnk" \
                   "$INSTDIR\${APP_EXE}" "" "$INSTDIR\${APP_EXE}" 0
SectionEnd

;-----------------------------------------------------------------------
; Uninstall section
;-----------------------------------------------------------------------
Section "Uninstall"
    Delete "$INSTDIR\${APP_EXE}"
    Delete "$INSTDIR\Uninstall.exe"
    RMDir  "$INSTDIR"

    Delete "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk"
    Delete "$SMPROGRAMS\${APP_NAME}\Uninstall.lnk"
    RMDir  "$SMPROGRAMS\${APP_NAME}"

    Delete "$DESKTOP\${APP_NAME}.lnk"

    DeleteRegKey HKLM "${REG_UNINSTALL}"
SectionEnd
