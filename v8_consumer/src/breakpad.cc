#include "../include/breakpad.h"
#include <iostream>
#include <string>

#if defined(BREAKPAD_FOUND) && defined(__linux__)
#include "client/linux/handler/exception_handler.h"
static bool dumpCallback(const google_breakpad::MinidumpDescriptor& descriptor,
                         void* context,
                         bool succeeded) {
    std::cerr << std::endl
              << "== Minidump location: " << descriptor.path()
              << " Status: " << succeeded << " ==" << std::endl;
    return succeeded;
}
void* setupBreakpad(const std::string& diagdir) {
    if (diagdir.length() < 1) return nullptr;
    google_breakpad::MinidumpDescriptor descriptor(diagdir.c_str());
    void* exceptionHandler = new google_breakpad::ExceptionHandler(
            descriptor,
            NULL,
            dumpCallback,
            NULL,
            true,
            -1);
    return exceptionHandler;
}

#elif defined(BREAKPAD_FOUND) && defined(_WIN32)
#include "client/windows/handler/exception_handler.h"
static bool dumpCallback(const wchar_t* dump_path,
                         const wchar_t* minidump_id,
                         void* context,
                         EXCEPTION_POINTERS* exinfo,
                         MDRawAssertionInfo* assertion,
                         bool succeeded) {
    std::wcerr << std::endl
               << "== Minidump location: " << dump_path
               << " ID: " << minidump_id << " Status: " << succeeded
               << " ==" << std::endl;
    return succeeded;
}
void* setupBreakpad(const std::string& diagdir) {
    if (diagdir.length() < 1) return nullptr;
    std::wstring path(diagdir.begin(), diagdir.end());
    MINIDUMP_TYPE dumptype = static_cast<MINIDUMP_TYPE>(
            MiniDumpWithFullMemory | MiniDumpWithProcessThreadData |
            MiniDumpWithHandleData);
    void* exceptionHandler = new google_breakpad::ExceptionHandler(
            path,
            nullptr,
            dumpCallback,
            nullptr,
            google_breakpad::ExceptionHandler::HANDLER_ALL,
            dumptype,
            (wchar_t*) nullptr,
            nullptr);
    return exceptionHandler;
}

#else
void* setupBreakpad(const std::string& diagdir) {
        return nullptr;
}
#endif