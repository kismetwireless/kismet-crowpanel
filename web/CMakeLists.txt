cmake_minimum_required(VERSION 3.16)
project(kismet_widget_web C CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

set(ROOT ${CMAKE_CURRENT_SOURCE_DIR}/..)
set(LVGL_DIR ${ROOT}/deps/lvgl)
set(AJSON_DIR ${ROOT}/deps/ArduinoJson)

file(GLOB_RECURSE LVGL_SRC ${LVGL_DIR}/src/*.c)
add_library(lvgl STATIC ${LVGL_SRC})
target_include_directories(lvgl PUBLIC ${LVGL_DIR} ${ROOT}/ui)
target_compile_definitions(lvgl PUBLIC LV_CONF_INCLUDE_SIMPLE)
target_compile_options(lvgl PRIVATE -O2 -w)

add_executable(widget
    main_web.cpp
    ${ROOT}/ui/widget_ui.cpp
    ${ROOT}/ui/widget_map.cpp
    ${ROOT}/ui/widget_icons.cpp
    ${ROOT}/ui/widget_model.cpp)
target_include_directories(widget PRIVATE ${ROOT}/ui ${AJSON_DIR}/src)
target_compile_options(widget PRIVATE -O2 -Wall -Wextra)
target_link_libraries(widget lvgl)
target_link_options(widget PRIVATE
    -O2
    -sALLOW_MEMORY_GROWTH=1
    -sENVIRONMENT=web
    -sEXPORTED_FUNCTIONS=_main,_widget_push_state,_widget_set_link,_widget_show_page,_widget_set_theme,_widget_pointer,_widget_tile,_widget_tile_failed,_widget_markers,_widget_track,_widget_detail,_widget_settings,_widget_wheel,_widget_pinch,_malloc,_free
    -sEXPORTED_RUNTIME_METHODS=ccall,HEAPU8)
