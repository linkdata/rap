#ifndef RAP_WRITER_HPP
#define RAP_WRITER_HPP

#include "rap.hpp"
#include "rap_text.hpp"

#include <cassert>
#include <cstdint>
#include <streambuf>

namespace rap {

class writer {
 public:
  typedef std::streambuf::traits_type traits_type;

  writer(std::streambuf &sb) : sb_(sb) {}

  error write_length(size_t n) const {
    if (n < 0x80) {
      sb_.sputc(static_cast<char>(n));
      return rap_err_ok;
    }
    if (n < 0x8000) {
      sb_.sputc(static_cast<char>((n >> 8) | 0x80));
      sb_.sputc(static_cast<char>(n));
      return rap_err_ok;
    }
    return rap_err_string_too_long;
  }

  error write_text(const char *src_ptr, size_t src_len) const {
    if (!src_len) {
      sb_.sputc(0);
      sb_.sputc(src_ptr ? 1 : 0);
      return rap_err_ok;
    }
    assert(src_ptr != NULL);
    if (unsigned key = rap_textmap_to_key(src_ptr, src_len)) {
      sb_.sputc(0);
      sb_.sputc(static_cast<char>(key));
      return rap_err_ok;
    }
    if (error e = write_length(static_cast<int16_t>(src_len))) return e;
    const char *src_end = src_ptr + src_len;
    while (src_ptr < src_end)
      if (sb_.sputc(*src_ptr++) == traits_type::eof()) {
        assert(0);
        return rap_err_output_buffer_too_small;
      }
    return rap_err_ok;
  }

  void write_uint64(uint64_t n) const {
    while (n >= 0x80) {
      sb_.sputc(static_cast<char>((n & 0x7f) | 0x80));
      n >>= 7;
    }
    sb_.sputc(static_cast<char>(n & 0x7f));
  }

  // single char
  const rap::writer &operator<<(char ch) const {
    sb_.sputc(ch);
    return *this;
  }

  // 16-bit MSB
  const rap::writer &operator<<(uint16_t n) const {
    sb_.sputc(static_cast<char>(n >> 8));
    sb_.sputc(static_cast<char>(n));
    return *this;
  }

  const rap::writer &operator<<(uint64_t n) const {
    write_uint64(n);
    return *this;
  }

  const rap::writer &operator<<(int64_t n) const {
    if (n >= 0)
      write_uint64((static_cast<uint64_t>(n) << 1));
    else
      write_uint64((static_cast<uint64_t>(-n) << 1) | 1);
    return *this;
  }

  const rap::writer &operator<<(const text &t) const {
    write_text(t.data(), t.size());
    return *this;
  }

  const rap::writer &operator<<(const std::string &s) const {
    write_text(s.data(), s.size());
    return *this;
  }

 private:
  std::streambuf &sb_;
};

}  // namespace rap

#endif  // RAP_WRITER_HPP
