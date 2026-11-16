cmake_minimum_required(VERSION 3.16)
project(ambivox_engine LANGUAGES C CXX)

option(AMBIVOX_BACKEND_SUSSURRO "Build the whisper.cpp + sussurro.cpp + sherpa-onnx backend" OFF)
option(AMBIVOX_BUILD_TESTS "Build the engine tests" ON)
set(AMBIVOX_SHERPA_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../third_party/sherpa-onnx" CACHE PATH
    "Prebuilt sherpa-onnx C API (include/ and lib/)")
set(AMBIVOX_SUSSURRO_TAG "298a657fc8710db844e0b82925b9e454b456fa81" CACHE STRING
    "sussurro.cpp revision")

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

if(NOT CMAKE_BUILD_TYPE)
    set(CMAKE_BUILD_TYPE Release)
endif()

set(AMBIVOX_SOURCES src/engine.cpp src/modem.cpp src/sim_backend.cpp)

if(AMBIVOX_BACKEND_SUSSURRO)
    if(CMAKE_VERSION VERSION_LESS 3.28)
        message(FATAL_ERROR "the sussurro backend needs CMake 3.28 or newer")
    endif()
    if(NOT EXISTS "${AMBIVOX_SHERPA_DIR}/include/sherpa-onnx/c-api/c-api.h")
        message(FATAL_ERROR
            "sherpa-onnx not found in ${AMBIVOX_SHERPA_DIR}; run scripts/setup-sussurro.sh first")
    endif()

    include(FetchContent)
    set(CMAKE_POLICY_VERSION_MINIMUM 3.5)
    set(BUILD_SHARED_LIBS OFF CACHE BOOL "" FORCE)
    FetchContent_Declare(
        sussurro
        GIT_REPOSITORY https://github.com/whispem/sussurro.cpp
        GIT_TAG ${AMBIVOX_SUSSURRO_TAG}
        GIT_SUBMODULES third_party/ggml third_party/whisper.cpp
        EXCLUDE_FROM_ALL
    )
    FetchContent_MakeAvailable(sussurro)

    add_library(ambivox_engine SHARED ${AMBIVOX_SOURCES} src/sussurro_backend.cpp)
    target_compile_definitions(ambivox_engine PRIVATE AMBIVOX_WITH_SUSSURRO)
    target_include_directories(ambivox_engine PRIVATE "${AMBIVOX_SHERPA_DIR}/include")
    target_link_directories(ambivox_engine PRIVATE "${AMBIVOX_SHERPA_DIR}/lib")
    target_link_libraries(ambivox_engine PRIVATE sussurro_core whisper sherpa-onnx-c-api)
    set_target_properties(ambivox_engine PROPERTIES
        BUILD_RPATH "${AMBIVOX_SHERPA_DIR}/lib"
        INSTALL_RPATH "${AMBIVOX_SHERPA_DIR}/lib"
        CXX_VISIBILITY_PRESET hidden)
    target_compile_definitions(ambivox_engine PRIVATE AMBIVOX_SHARED)
else()
    add_library(ambivox_engine STATIC ${AMBIVOX_SOURCES})
endif()

target_include_directories(ambivox_engine PUBLIC include PRIVATE src)

if(CMAKE_CXX_COMPILER_ID MATCHES "GNU|Clang")
    target_compile_options(ambivox_engine PRIVATE -Wall -Wextra)
endif()

install(TARGETS ambivox_engine ARCHIVE DESTINATION lib LIBRARY DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)

if(AMBIVOX_BUILD_TESTS)
    enable_testing()
    add_executable(engine_test tests/engine_test.cpp src/modem.cpp)
    target_include_directories(engine_test PRIVATE src)
    target_link_libraries(engine_test PRIVATE ambivox_engine)
    add_test(NAME engine_test COMMAND engine_test)
endif()
