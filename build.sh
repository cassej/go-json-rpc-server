#!/bin/bash

# Устанавливаем начальную директорию для поиска исходных файлов
SRC_DIR="methods"
# Устанавливаем целевую директорию для скомпилированных плагинов
BUILD_DIR="methods"

# Создаем целевую директорию, если она не существует
mkdir -p "$BUILD_DIR"

# Функция для компиляции каждого файла Go в плагин
compile_plugin() {
    local src_file="$1"
    local dest_file="${src_file/$SRC_DIR/$BUILD_DIR}" # Заменяем начальную часть пути
    dest_file="${dest_file%.go}.so" # Заменяем расширение .go на .so

    local dest_dir=$(dirname "$dest_file")
    mkdir -p "$dest_dir" # Создаем целевую директорию, если она не существует

    echo "Compiling $src_file to $dest_file"
    go build -buildmode=plugin -o "$dest_file" "$src_file"
}

export -f compile_plugin
export SRC_DIR
export BUILD_DIR

# Находим все файлы Go в SRC_DIR и компилируем их в плагины
find "$SRC_DIR" -type f -name "*.go" -exec bash -c 'compile_plugin "$0"' {} \;
