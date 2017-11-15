#ifndef RAP_HEAD_HPP
#define RAP_HEAD_HPP

#include "rap.hpp"
#include "rap_text.hpp"
#include "rap_frame.hpp"

#include <cassert>
#include <streambuf>

namespace rap
{

class record
{
public:
  typedef char tag;

  enum
  {
    tag_http_request = tag('\x01'),
    tag_http_response = tag('\x02'),
    tag_unknown = tag(0)
  } tags;

  static void write(std::streambuf &sb, char ch)
  {
    sb.sputc(ch);
  }

  static void write(std::streambuf &sb, uint16_t n)
  {
    sb.sputc(static_cast<char>(n >> 8));
    sb.sputc(static_cast<char>(n));
  }

  explicit record(const rap::frame *f)
      : frame_(f)
  {
  }

protected:
  const rap::frame *frame_;
};

} // namespace rap

#endif // RAP_HEAD_HPP
