cmake_minimum_required(VERSION 2.8.10)

set(CMAKE_VERBOSE_MAKEFILE OFF)
set(CMAKE_CONFIGURATION_TYPES Debug Release)

project(rapper)
enable_testing()

link_directories(${CMAKE_ARCHIVE_OUTPUT_DIRECTORY})

if(WIN32)
    add_definitions(-DWIN32_LEAN_AND_MEAN -D_WIN32_WINNT=0x0601 -D_CRT_SECURE_NO_WARNINGS)
endif()

set(Boost_ADDITIONAL_VERSIONS 1.59)
set(Boost_USE_STATIC_LIBS ON)
find_package(Boost REQUIRED COMPONENTS system)
include_directories(${Boost_INCLUDE_DIRS})
link_directories(${Boost_LIBRARY_DIRS})
if(Boost_FOUND)
    add_definitions(-DHAVE_BOOST)
endif()

# build the sample echo server 'rapper'
include_directories(${CMAKE_CURRENT_SOURCE_DIR})
add_executable(rapper
  rap.hpp
  rap_conn.hpp
  rap_exchange.hpp
  rap_frame.hpp
  rap_header.hpp
  rap_kvv.hpp
  rap_reader.hpp
  rap_record.hpp
  rap_request.hpp
  rap_response.hpp
  rap_server.hpp
  rap_text.hpp
  rap_textmap.c
  rap_textmap.gperf
  rap_writer.hpp

  rapper/rapper.hpp
  rapper/rapper_conn.hpp
  rapper/rapper_server.hpp
  rapper/rapper.cpp
)
target_link_libraries(rapper ${Boost_LIBRARIES})
