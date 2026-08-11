Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows you to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
## 
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
####
## The following information is taken from the wails_tools.nsh file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "my-project" # Default "koyori-ide"
## !define INFO_COMPANYNAME    "My Company" # Default "koyoriIde"
## !define INFO_PRODUCTNAME    "My Product Name" # Default "koyori-ide"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "0.1.0"
## !define INFO_COPYRIGHT      "(c) Now, My Company" # Default "© 2026, My Company"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
## !define WAILS_INSTALL_SCOPE     "user"             # Default "machine" - set to "user" for per-user install ($LOCALAPPDATA) without UAC prompt
####
## Include the wails tools
####
!include "wails_tools.nsh"

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uninstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
!if "${WAILS_INSTALL_SCOPE}" == "user"
    InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
!else
    InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
!endif
ShowInstDetails show # This will always show the installation details.

Function .onInit
   !insertmacro wails.checkArchitecture
FunctionEnd

Function un.ValidateInstallDir
    StrCmp "$INSTDIR" "" unsafe

    # Canonicalise the path before checking roots and protected directories.
    GetFullPathName $0 "$INSTDIR"
    StrCmp "$0" "" unsafe

    # Network paths are not valid installation targets for this installer.
    # Reject all UNC paths, including share roots, rather than trying to infer
    # a safe deletion boundary on a remote filesystem.
    StrCpy $1 "$0" 2
    StrCmp "$1" "\\" unsafe

    # Reject drive roots such as C:\.
    StrCpy $1 "$0" 1 1
    StrCmp "$1" ":" 0 trim_trailing_separators
    StrCpy $1 "$0" 1 2
    StrCmp "$1" "\" 0 trim_trailing_separators
    StrCpy $1 "$0" "" 3
    StrCmp "$1" "" unsafe

trim_trailing_separators:
    # Normalise non-root paths such as C:\Program Files\ before exact checks.
    StrCpy $1 "$0" 1 -1
    StrCmp "$1" "\" 0 check_system_directories
    StrCpy $0 "$0" -1
    Goto trim_trailing_separators

check_system_directories:
    # Reject Windows/System32 themselves and every descendant, while allowing
    # similarly prefixed siblings such as C:\Windows.old.
    StrLen $1 "$WINDIR"
    StrCpy $2 "$0" $1
    StrCmp "$2" "$WINDIR" 0 check_system_tree
    StrCpy $2 "$0" 1 $1
    StrCmp "$2" "" unsafe
    StrCmp "$2" "\" unsafe

check_system_tree:
    StrLen $1 "$SYSDIR"
    StrCpy $2 "$0" $1
    StrCmp "$2" "$SYSDIR" 0 check_exact_system_directories
    StrCpy $2 "$0" 1 $1
    StrCmp "$2" "" unsafe
    StrCmp "$2" "\" unsafe

check_exact_system_directories:
    # Other protected roots are rejected exactly. Their application
    # subdirectories (for example Program Files\Vendor\App) remain valid.
    StrCmp "$0" "$PROGRAMFILES" unsafe
    StrCmp "$0" "$PROGRAMFILES64" unsafe
    StrCmp "$0" "$COMMONFILES" unsafe
    StrCmp "$0" "$COMMONFILES64" unsafe
    StrCmp "$0" "$PROFILE" unsafe
    StrCmp "$0" "$APPDATA" unsafe
    StrCmp "$0" "$LOCALAPPDATA" unsafe
    StrCmp "$0" "$TEMP" unsafe
    Return

unsafe:
    MessageBox MB_OK|MB_ICONSTOP "Uninstall aborted: the installation directory is unsafe: $INSTDIR"
    Abort
FunctionEnd

Section
    !insertmacro wails.setShellContext

    !insertmacro wails.webview2runtime

    SetOutPath $INSTDIR
    
    !insertmacro wails.files

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols
    
    !insertmacro wails.writeUninstaller
SectionEnd

Section "uninstall" 
    !insertmacro wails.setShellContext

    Call un.ValidateInstallDir

    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    !insertmacro wails.deleteUninstaller
SectionEnd
